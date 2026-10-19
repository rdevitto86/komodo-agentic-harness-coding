package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuilderRole(t *testing.T) {
	r := BuilderRole()
	if r.Name != "builder" || !r.PoolSlot {
		t.Fatalf("role = %+v", r)
	}
	if !reflect.DeepEqual(r.Tools, []string{"read", "search", "edit", "write", "run", "skill", "task_done"}) {
		t.Errorf("tools = %v", r.Tools)
	}
	if !reflect.DeepEqual(r.RunClasses, []string{"build", "test", "lint", "format"}) ||
		!reflect.DeepEqual(r.Guard, []string{"R1", "B1"}) {
		t.Errorf("classes %v guard %v", r.RunClasses, r.Guard)
	}
	if len(r.Slots.Required) != 5 || len(r.Slots.Optional) != 8 {
		t.Errorf("slots = %+v", r.Slots)
	}
	if !strings.Contains(string(r.Result), "komodo:agent/builder/result/1") {
		t.Errorf("result schema = %s", r.Result)
	}
}

func TestBuilderRoleIsACopy(t *testing.T) {
	r := BuilderRole()
	r.Tools[0] = "changed"
	if BuilderRole().Tools[0] != "read" {
		t.Fatal("a caller changed the compiled role")
	}
}

func TestParseBuilderResult(t *testing.T) {
	ok := []struct{ name, raw string }{
		{
			"done",
			`{"status": "done", "summary": "Added the lock file type and its tests; both tasks pass.", "disputed": []}`,
		},
		{
			"scope",
			`{"status": "needs_scope", "summary": "Stopped before tsk-add-lock.", "paths": ["internal/lease/lock.go"], ` +
				`"reason": "The lock type must be read by the lease package."}`,
		},
		{"disputed", `{"status": "done", "summary": "s", "disputed": ["internal/agent/lock.go:42"]}`},
	}
	for _, c := range ok {
		if _, err := ParseBuilderResult([]byte(c.raw)); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	bad := []struct{ name, raw string }{
		{"scope without paths", `{"status": "needs_scope", "summary": "s", "reason": "r"}`},
		{"scope without reason", `{"status": "needs_scope", "summary": "s", "paths": ["a.go"]}`},
		{"done with paths", `{"status": "done", "summary": "s", "paths": ["a.go"]}`},
		{"done with reason", `{"status": "done", "summary": "s", "reason": "r"}`},
		{"bad status", `{"status": "finished", "summary": "s"}`},
		{"no summary", `{"status": "done", "summary": "  "}`},
		{"unknown field", `{"status": "done", "summary": "s", "extra": 1}`},
		{"bad disputed", `{"status": "done", "summary": "s", "disputed": ["no-line"]}`},
		{"empty path", `{"status": "needs_scope", "summary": "s", "paths": [""], "reason": "r"}`},
		{"trailing data", `{"status": "done", "summary": "s"} {}`},
		{"not json", `done`},
	}
	for _, c := range bad {
		if _, err := ParseBuilderResult([]byte(c.raw)); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestOutOfScope(t *testing.T) {
	files := []string{"internal/agent/lock.go", "docs/**"}
	sibling := func(p string) bool {
		return strings.HasSuffix(p, "_test.go") && strings.HasPrefix(p, "internal/agent/")
	}
	got := OutOfScope(
		[]string{"internal/agent/lock.go", "internal/agent/lock_test.go", "docs/a/b.md", "internal/lease/x.go"},
		files,
		sibling,
		MatchGlob,
	)
	if !reflect.DeepEqual(got, []string{"internal/lease/x.go"}) {
		t.Fatalf("out of scope = %v", got)
	}
	if got := OutOfScope([]string{"a.go"}, nil, nil, nil); !reflect.DeepEqual(got, []string{"a.go"}) {
		t.Fatalf("nil funcs: %v", got)
	}
	if got := OutOfScope(nil, files, sibling, MatchGlob); len(got) != 0 {
		t.Fatalf("no changes: %v", got)
	}
}

func TestAutoDeny(t *testing.T) {
	protected := func(p string) bool { return p == ".git" || strings.HasPrefix(p, ".git/") }
	other := func(p string) (string, bool) { return "tg-1.2", p == "internal/lease/lock.go" }
	if p, why, d := AutoDeny([]string{"a.go", ".git/config"}, protected, other); !d || p != ".git/config" || why == "" {
		t.Errorf("protected: %q %q %v", p, why, d)
	}
	p, why, d := AutoDeny([]string{"internal/lease/lock.go"}, protected, other)
	if !d || p != "internal/lease/lock.go" || !strings.Contains(why, "tg-1.2") {
		t.Errorf("other group: %q %q %v", p, why, d)
	}
	if _, _, d := AutoDeny([]string{"a.go", "b.go"}, protected, other); d {
		t.Error("allowed paths were denied")
	}
	if _, _, d := AutoDeny([]string{".git"}, nil, nil); d {
		t.Error("nil callbacks denied a path")
	}
}

func TestTaskGateComplete(t *testing.T) {
	var ran []string
	run := func(exit int, out string) CommandRunner {
		return func(_ context.Context, c string) (int, string, error) { ran = append(ran, c); return exit, out, nil }
	}
	ref := TaskRef{Group: "tg-1", Task: "tsk-a", DoneCommand: "go test ./..."}
	o, err := TaskGate{Run: run(0, "ok")}.Complete(context.Background(), ref)
	if err != nil || o.ExitCode != 0 || o.Output != "ok" {
		t.Fatalf("exit 0: %+v %v", o, err)
	}
	o, err = TaskGate{Run: run(1, "FAIL")}.Complete(context.Background(), ref)
	if err != nil || o.ExitCode != 1 || o.Output != "FAIL" {
		t.Fatalf("exit 1: %+v %v", o, err)
	}
	if !reflect.DeepEqual(ran, []string{"go test ./...", "go test ./..."}) {
		t.Fatalf("commands run = %v", ran)
	}
	boom := errors.New("sandbox refused")
	_, err = TaskGate{
		Run: func(context.Context, string) (int, string, error) { return 0, "", boom },
	}.Complete(context.Background(), ref)
	if !errors.Is(err, boom) {
		t.Fatalf("runner error = %v", err)
	}
	if _, err := (TaskGate{}).Complete(context.Background(), ref); err == nil {
		t.Error("nil runner accepted")
	}
	if _, err := (TaskGate{Run: run(0, "")}).Complete(context.Background(), TaskRef{Task: "tsk-a"}); err == nil {
		t.Error("empty completion command accepted")
	}
}

func TestTaskGateCapsOutput(t *testing.T) {
	big := strings.Repeat("a", 3000) + "MIDDLE" + strings.Repeat("z", 3000)
	o, _ := TaskGate{
		Run: func(context.Context, string) (int, string, error) { return 1, big, nil },
	}.Complete(context.Background(), TaskRef{Task: "t", DoneCommand: "x"})
	if len(o.Output) > GateOutputCap || !strings.HasPrefix(o.Output, "aaa") || !strings.HasSuffix(o.Output, "zzz") ||
		strings.Contains(o.Output, "MIDDLE") {
		t.Fatalf("capped output len %d", len(o.Output))
	}
}

func TestCapOutputKeepsRunes(t *testing.T) {
	s := strings.Repeat("é", 500)
	got := CapOutput(s, 101)
	if len(got) > 101 {
		t.Fatalf("len %d", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("cut inside a character")
		}
	}
	if CapOutput("short", 100) != "short" {
		t.Fatal("short output changed")
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"**/*.go", "a/b/c.go", true}, {"**/*.go", "c.go", true}, {"**/*.go", "c.md", false},
		{"internal/**", "internal/a/b.go", true}, {"internal/*", "internal/a/b.go", false},
		{"*.md", "docs/a.md", true}, {"lock.go", "internal/lock.go", true},
		{"internal/a?.go", "internal/ab.go", true}, {"embed/**/*.json", "embed/agents/x/agent.json", true},
		{"**/*_darwin.go", "internal/x/f_darwin.go", true}, {"docs/**", "docs", true}, {"[", "a", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.glob, c.path, got)
		}
	}
}
