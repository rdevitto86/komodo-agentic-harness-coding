package task

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// fakeConfiner runs commands on the host in the worktree, standing in for the sandbox manager.
type fakeConfiner struct {
	dir     string
	mu      sync.Mutex
	masked  []string
	started int
	env     []string // added to the environment of every command
}

// SetMasked records the last masked list.
func (f *fakeConfiner) SetMasked(_ string, m []string) error {
	f.mu.Lock()
	f.masked = m
	f.mu.Unlock()
	return nil
}

// Command builds a host command in its own process group.
func (f *fakeConfiner) Command(ctx context.Context, _ string, c sandbox.Command) (*exec.Cmd, error) {
	f.mu.Lock()
	f.started++
	f.mu.Unlock()
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = f.dir
	if len(f.env) > 0 || len(c.Env) > 0 {
		cmd.Env = append(append(os.Environ(), f.env...), c.Env...)
	}
	killGroupOnCancel(cmd)
	return cmd, nil
}

// fakeGit returns fixed text.
type fakeGit struct{ diff, read string }

// Diff returns the fixed diff.
func (g fakeGit) Diff(context.Context, []string) (string, error) { return g.diff, nil }

// ReadOnly returns the fixed text.
func (g fakeGit) ReadOnly(context.Context, string, []string) (string, error) { return g.read, nil }

// rig is a bound tool set over a fixture worktree.
type rig struct {
	f     fixture
	env   *Env
	b     *Bound
	conf  *fakeConfiner
	facts []Fact
	mu    sync.Mutex
}

// allTools lists every built-in tool.
var allTools = []string{
	"read",
	"search",
	"edit",
	"write",
	"run",
	"diff",
	"git_read",
	"skill",
	"submit_finding",
	"task_done",
	"lint_plan",
	"run_check",
}

// newRig binds every tool to a builder-like guard whose files list covers the whole tree.
func newRig(t *testing.T, mutate func(*Env)) *rig {
	t.Helper()
	f := newFixture(t)
	r := &rig{f: f, conf: &fakeConfiner{dir: f.Root}}
	r.env = &Env{Worktree: f.Root, Dir: filepath.Join(t.TempDir(), "session"), Confiner: r.conf, Instance: "i1",
		Commands: map[string][]string{
			"build": {"true"},
		}, OnFact: func(x Fact) { r.mu.Lock(); r.facts = append(r.facts, x); r.mu.Unlock() }}
	if err := os.MkdirAll(r.env.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(r.env)
	}
	perms := Permissions{Tools: allTools, RunClasses: []string{"build", "test", "deploy"}, Guard: []RowID{"R1", "B1"}}
	g := newTestGuard(t, f, "builder", perms, []string{"**"}, 3)
	b, err := NewRegistry(r.env).Bind(allTools, g)
	if err != nil {
		t.Fatal(err)
	}
	r.b = b
	return r
}

// call dispatches one tool call.
func (r *rig) call(tool string, args any) Reply {
	b, _ := json.Marshal(args)
	return r.b.Dispatch(context.Background(), tool, b)
}

func TestBindFailsOnAnUnregisteredTool(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "builder", builderPerms(), nil, 3)
	if _, err := NewRegistry(&Env{Worktree: f.Root}).Bind([]string{"read", "teleport"}, g); err == nil ||
		!strings.Contains(err.Error(), "teleport") {
		t.Fatalf("Bind error = %v, want one naming teleport", err)
	}
}

func TestKnownMatchesRegistry(t *testing.T) {
	tools, rows := KnownTools(), KnownRows()
	if len(tools) != len(allTools) || len(rows) != 8 {
		t.Fatalf("known: %d tools, %d rows", len(tools), len(rows))
	}
	for _, n := range allTools {
		if !tools[n] {
			t.Errorf("Known lacks the tool %s", n)
		}
	}
	var host llm.ToolHost = &Bound{}
	_ = host
}

func TestAdvertisedIsThePermittedSet(t *testing.T) {
	f := newFixture(t)
	g := newTestGuard(t, f, "reviewer", reviewerPerms(), nil, 3)
	b, err := NewRegistry(&Env{Worktree: f.Root}).Bind(reviewerPerms().Tools, g)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.Advertised(), ","); got != strings.Join(reviewerPerms().Tools, ",") {
		t.Fatalf("Advertised = %s", got)
	}
	if _, ok := b.Definition("edit"); ok {
		t.Error("a tool the agent may not call has a definition")
	}
	if d, ok := b.Definition("read"); !ok || d.Name != "read" || !json.Valid(d.InputSchema) {
		t.Errorf("read definition = %+v %v", d, ok)
	}
}

func TestDispatchRefusalIsAReplyAndAFact(t *testing.T) {
	r := newRig(t, nil)
	reply := r.call("read", map[string]any{"path": "../outside/secret.txt"})
	if !strings.Contains(reply.Text, "G2") || !strings.Contains(reply.Text, "worktree") {
		t.Fatalf("refusal reply = %q", reply.Text)
	}
	if len(r.facts) != 1 || r.facts[0].Subtype != "tool.denied" {
		t.Fatalf("facts = %+v", r.facts)
	}
	var d struct {
		Tool, Row, Target string
		Count             int
	}
	if err := json.Unmarshal(r.facts[0].Data, &d); err != nil || d.Row != "G2" || d.Count != 1 || d.Tool != "read" {
		t.Fatalf("fact data = %s (%v)", r.facts[0].Data, err)
	}
	lines := toolLines(t, r.env.Dir)
	if len(lines) < 2 || lines[0].Kind != "start" || lines[1].Kind != "denied" {
		t.Fatalf("tools.jsonl = %+v", lines)
	}
	if lines[0].At.Nanosecond() != 0 {
		t.Error("tools.jsonl times must be whole seconds")
	}
}

func TestReadNumbersLinesAndIsNotCapped(t *testing.T) {
	r := newRig(t, nil)
	var b strings.Builder
	for i := 1; i <= 2500; i++ {
		b.WriteString("line of text number\n")
	}
	writeFile(t, filepath.Join(r.f.Root, "big.txt"), b.String())
	got := r.call("read", map[string]any{"path": "big.txt"})
	if !strings.HasPrefix(got.Text, "1\tline of text number\n") || !strings.Contains(got.Text, "2000\tline") ||
		strings.Contains(got.Text, "2001\tline") {
		t.Fatalf("read start = %.60q ...", got.Text)
	}
	if len(got.Text) <= replyCap || got.LogPath != "" || !strings.Contains(got.Text, "start=2001") {
		t.Fatalf("read must not be capped and must say where to go on: len %d", len(got.Text))
	}
	part := r.call("read", map[string]any{"path": "big.txt", "start": 3, "end": 4})
	if part.Text != "3\tline of text number\n4\tline of text number\n" {
		t.Fatalf("read 3..4 = %q", part.Text)
	}
	if e := r.call("read", map[string]any{"path": "internal"}); !strings.HasPrefix(e.Text, "error:") {
		t.Errorf("read of a folder = %q", e.Text)
	}
	writeFile(t, filepath.Join(r.f.Root, "bin.dat"), "a\x00b")
	if e := r.call("read", map[string]any{"path": "bin.dat"}); !strings.Contains(e.Text, "binary") {
		t.Errorf("read of a binary = %q", e.Text)
	}
}

func TestSearchSortsAndSkipsSecretsLinksAndGit(t *testing.T) {
	r := newRig(t, nil)
	writeFile(t, filepath.Join(r.f.Root, "z.go"), "needle one\nnothing\nneedle two\n")
	writeFile(t, filepath.Join(r.f.Root, "a/b.go"), "needle in a\n")
	writeFile(t, filepath.Join(r.f.Root, ".env"), "needle=secret\n")
	writeFile(t, filepath.Join(r.f.Outside, "o.txt"), "needle outside\n")
	got := r.call("search", map[string]any{"pattern": "needle"})
	want := "a/b.go:1: needle in a\nz.go:1: needle one\nz.go:3: needle two\n"
	if got.Text != want {
		t.Fatalf("search = %q, want %q", got.Text, want)
	}
	if e := r.call("search", map[string]any{"pattern": "("}); !strings.HasPrefix(e.Text, "error:") {
		t.Errorf("bad regexp = %q", e.Text)
	}
	if n := r.call("search", map[string]any{"pattern": "zzzzzz"}); n.Text != "no matches" {
		t.Errorf("no match = %q", n.Text)
	}
}

func TestEditReplacesOnceAndShowsContext(t *testing.T) {
	r := newRig(t, nil)
	writeFile(t, filepath.Join(r.f.Root, "e.go"), "l1\nl2\nl3\nTARGET\nl5\nl6\nl7\nl8\n")
	got := r.call("edit", map[string]any{"path": "e.go", "old": "TARGET", "new": "CHANGED"})
	if !strings.Contains(got.Text, "4\tCHANGED") || !strings.Contains(got.Text, "1\tl1") ||
		!strings.Contains(got.Text, "7\tl7") || strings.Contains(got.Text, "8\tl8") {
		t.Fatalf("edit reply = %q", got.Text)
	}
	if b, _ := os.ReadFile(filepath.Join(r.f.Root, "e.go")); !strings.Contains(string(b), "CHANGED") {
		t.Fatal("file not edited")
	}
	nf := r.call("edit", map[string]any{"path": "e.go", "old": "TARGET", "new": "x"})
	if !strings.HasPrefix(nf.Text, "error:") || !strings.Contains(nf.Text, "closest") {
		t.Fatalf("missing text reply = %q", nf.Text)
	}
	writeFile(t, filepath.Join(r.f.Root, "dup.go"), "same\nsame\n")
	if d := r.call("edit", map[string]any{
		"path": "dup.go",
		"old":  "same",
		"new":  "x",
	}); !strings.Contains(d.Text, "2 times") {
		t.Fatalf("duplicate reply = %q", d.Text)
	}
	if n := r.call("edit", map[string]any{
		"path": "absent.go",
		"old":  "a",
		"new":  "b",
	}); !strings.Contains(n.Text, "does not exist") {
		t.Fatalf("absent file reply = %q", n.Text)
	}
}

func TestEditClosestLinesNameFiveCandidates(t *testing.T) {
	r := newRig(t, nil)
	writeFile(t, filepath.Join(r.f.Root, "c.go"), strings.Repeat("func Handle(a int) error {\n", 8))
	got := r.call("edit", map[string]any{"path": "c.go", "old": "func Handle(a int32) error {", "new": "x"})
	if n := strings.Count(got.Text, "func Handle"); n != closestLines {
		t.Fatalf("closest lines shown = %d, want %d:\n%s", n, closestLines, got.Text)
	}
}

func TestEditFormatsInTheConfinement(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Format = []string{"sh", "-c", "echo \"$1\" > fmt.marker", "fmt"} })
	writeFile(t, filepath.Join(r.f.Root, "f.go"), "a\n")
	r.call("edit", map[string]any{"path": "f.go", "old": "a", "new": "b"})
	if b, _ := os.ReadFile(filepath.Join(r.f.Root, "fmt.marker")); strings.TrimSpace(string(b)) != "f.go" {
		t.Fatalf("format command saw %q", b)
	}
	if r.conf.started != 1 {
		t.Fatalf("commands started = %d", r.conf.started)
	}
}

func TestEditKeepsTheEditWhenFormatFails(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Format = []string{"sh", "-c", "echo boom >&2; exit 3", "fmt"} })
	writeFile(t, filepath.Join(r.f.Root, "f.go"), "a\n")
	got := r.call("edit", map[string]any{"path": "f.go", "old": "a", "new": "b"})
	if !strings.Contains(got.Text, "not formatted") || !strings.Contains(got.Text, "boom") ||
		!strings.Contains(got.Text, "1\tb") {
		t.Fatalf("reply = %q", got.Text)
	}
	if b, _ := os.ReadFile(filepath.Join(r.f.Root, "f.go")); string(b) != "b\n" {
		t.Fatalf("file = %q", b)
	}
}

func TestWriteCreatesAndRefusesExisting(t *testing.T) {
	r := newRig(t, nil)
	got := r.call("write", map[string]any{"path": "pkg/new/x.go", "content": "a\nb\n"})
	if got.Text != "created pkg/new/x.go, 2 lines" {
		t.Fatalf("write reply = %q", got.Text)
	}
	again := r.call("write", map[string]any{"path": "pkg/new/x.go", "content": "z"})
	if !strings.HasPrefix(again.Text, "error:") || !strings.Contains(again.Text, "use edit") {
		t.Fatalf("second write = %q", again.Text)
	}
	if b, _ := os.ReadFile(filepath.Join(r.f.Root, "pkg/new/x.go")); string(b) != "a\nb\n" {
		t.Fatalf("existing file was changed: %q", b)
	}
}

func TestRunPassesArgumentsAsLiterals(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Commands["build"] = []string{"printf", "[%s]\n"} })
	got := r.call("run", map[string]any{"class": "build", "args": []string{"&&", "rm -rf /", "$(id)", ";"}})
	if got.Text != "[&&]\n[rm -rf /]\n[$(id)]\n[;]\n" {
		t.Fatalf("run output = %q", got.Text)
	}
}

func TestRunCapsOutputAndKeepsTheLog(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Commands["build"] = []string{"sh", "-c", "i=0; while [ $i -lt 4000 ]; do echo 0123456789; i=$((i+1)); done"}
	})
	got := r.call("run", map[string]any{"class": "build"})
	if len(got.Text) > replyCap || got.LogPath == "" {
		t.Fatalf("reply length %d, log %q", len(got.Text), got.LogPath)
	}
	if !strings.Contains(got.Text, "bytes cut; full output in "+got.LogPath) {
		t.Fatalf("reply does not name its log: %.300q", got.Text)
	}
	if !strings.HasPrefix(got.Text, "0123456789\n") || !strings.HasSuffix(got.Text, "0123456789\n") {
		t.Fatal("reply must keep the start and the end of the output")
	}
	b, err := os.ReadFile(got.LogPath)
	if err != nil || len(b) != 4000*11 {
		t.Fatalf("log: %d bytes, %v; want %d", len(b), err, 4000*11)
	}
	if got.LogPath != filepath.Join(r.env.Dir, "out", "c1.log") {
		t.Errorf("log path = %s", got.LogPath)
	}
}

func TestRunReportsExitStatusAndShortOutput(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Commands["build"] = []string{"sh", "-c", "echo bad >&2; exit 3"} })
	got := r.call("run", map[string]any{"class": "build"})
	if got.Text != "exit status 3\nbad\n" || got.LogPath != "" {
		t.Fatalf("reply = %q log %q", got.Text, got.LogPath)
	}
}

func TestRunKillsACommandPastTheTimeout(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.RunTimeout = 300 * time.Millisecond
		e.Commands["build"] = []string{"sh", "-c", "echo started; sleep 20"}
	})
	t0 := time.Now()
	got := r.call("run", map[string]any{"class": "build"})
	if time.Since(t0) > 10*time.Second || !strings.HasPrefix(got.Text, timeoutLine) ||
		!strings.Contains(got.Text, "started") {
		t.Fatalf("after %s: %q", time.Since(t0), got.Text)
	}
	lines := toolLines(t, r.env.Dir)
	if last := lines[len(lines)-1]; last.Kind != "end" || last.Outcome != "timeout" {
		t.Fatalf("end line = %+v", last)
	}
}

func TestRunMasksSecretFilesAndNeverRunsUnconfined(t *testing.T) {
	r := newRig(t, nil)
	r.call("run", map[string]any{"class": "build"})
	if len(r.conf.masked) == 0 || !strings.HasSuffix(r.conf.masked[0], ".env") {
		t.Fatalf("masked = %v", r.conf.masked)
	}
	none := newRig(t, func(e *Env) { e.Confiner = nil })
	got := none.call("run", map[string]any{"class": "build"})
	if !strings.HasPrefix(got.Text, "error:") || !strings.Contains(got.Text, "no sandbox") {
		t.Fatalf("run without a sandbox = %q", got.Text)
	}
	if e := r.call("run", map[string]any{"class": "test"}); !strings.Contains(e.Text, "no command") {
		t.Errorf("class without a command = %q", e.Text)
	}
	if e := r.call("run", map[string]any{"class": "lint"}); !strings.Contains(e.Text, "R1") {
		t.Errorf("class not permitted = %q", e.Text)
	}
}

func TestLongReplyOfAnyOtherToolIsCappedToALog(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 1024)
	r := newRig(t, func(e *Env) { e.Skill = func(string) (string, error) { return big, nil } })
	got := r.call("skill", map[string]any{"name": "standards-go"})
	if len(got.Text) > replyCap || got.LogPath == "" {
		t.Fatalf("len %d log %q", len(got.Text), got.LogPath)
	}
	if b, _ := os.ReadFile(got.LogPath); string(b) != big {
		t.Fatal("log must hold the whole text")
	}
}

func TestSubmitFindingFilesAFact(t *testing.T) {
	var got []Submitted
	r := newRig(t, func(e *Env) { e.OnFinding = func(s Submitted) { got = append(got, s) } })
	reply := r.call("submit_finding", map[string]any{
		"rule":  "E1",
		"file":  "cmd/main.go",
		"line":  1,
		"quote": "package main",
		"note":  "n",

		"confidence": 90,
	})
	if reply.Text != "filed E1 at cmd/main.go:1" || len(got) != 1 || got[0].Line != 1 || got[0].Confidence != 90 {
		t.Fatalf("reply %q findings %+v", reply.Text, got)
	}
	if f := r.facts[len(r.facts)-1]; f.Subtype != "finding.submit" || !strings.Contains(string(f.Data), `"rule":"E1"`) {
		t.Fatalf("fact = %+v", f)
	}
	if bad := r.call("submit_finding", map[string]any{
		"rule":  "",
		"file":  "cmd/main.go",
		"line":  0,
		"quote": "",
		"note":  "",
	}); !strings.HasPrefix(bad.Text, "error:") {
		t.Errorf("empty finding = %q", bad.Text)
	}
}

func TestSubmitFindingCarriesItsProof(t *testing.T) {
	var got []Submitted
	r := newRig(t, func(e *Env) { e.OnFinding = func(s Submitted) { got = append(got, s) } })
	r.call("submit_finding", map[string]any{
		"rule":  "correctness.2",
		"file":  "cmd/main.go",
		"line":  1,
		"quote": "package main",
		"note":  "n",

		"confidence": 85,
		"proof":      map[string]any{"ran": "go test ./cmd/ -run TestDrop", "saw": "--- FAIL: TestDrop"},
	})
	if len(got) != 1 || !got[0].Proof.Present() || got[0].Proof.Saw != "--- FAIL: TestDrop" ||
		got[0].Proof.Ran != "go test ./cmd/ -run TestDrop" {
		t.Fatalf("findings %+v", got)
	}
	if f := r.facts[len(r.facts)-1]; !strings.Contains(string(f.Data), `"saw":"--- FAIL: TestDrop"`) ||
		!strings.Contains(string(f.Data), `"confidence":85`) {
		t.Errorf("fact = %s", f.Data)
	}
}

func TestSubmitFindingRefusesAMissingOrOutOfRangeConfidence(t *testing.T) {
	var got []Submitted
	r := newRig(t, func(e *Env) { e.OnFinding = func(s Submitted) { got = append(got, s) } })
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"rule": "E1", "file": "cmd/main.go", "line": 1, "quote": "package main", "note": "n"}
		maps.Copy(m, extra)
		return m
	}

	for name, extra := range map[string]map[string]any{
		"missing": nil, "above 100": {"confidence": 101}, "below 0": {"confidence": -1},
	} {
		if reply := r.call("submit_finding", base(extra)); !strings.HasPrefix(reply.Text, "error:") {
			t.Errorf("%s: reply %q", name, reply.Text)
		}
	}
	for _, n := range []int{0, 100} {
		reply := r.call("submit_finding", base(map[string]any{"confidence": n}))
		if strings.HasPrefix(reply.Text, "error:") {
			t.Errorf("confidence %d: reply %q", n, reply.Text)
		}
	}
	if len(got) != 2 {
		t.Errorf("filed %d findings, want 2", len(got))
	}
}

func TestTaskDoneLintAndSkill(t *testing.T) {
	r := newRig(t, func(e *Env) {
		e.Done = func(_ context.Context, task string) (int, string, error) { return 0, "ok " + task, nil }
		e.Lint = func(d string) []string {
			if d == "bad" {
				return []string{"line 1: x", "line 2: y"}
			}
			return nil
		}
		e.Skill = func(ref string) (string, error) {
			if ref == "nope" {
				return "", errors.New("no such skill")
			}
			return "skill " + ref, nil
		}
	})
	if got := r.call("task_done", map[string]any{"task": "t1"}); got.Text != "exit code 0\nok t1" {
		t.Errorf("task_done = %q", got.Text)
	}
	if got := r.call("lint_plan", map[string]any{"draft": "good"}); got.Text != "ok" {
		t.Errorf("lint ok = %q", got.Text)
	}
	if got := r.call("lint_plan", map[string]any{"draft": "bad"}); got.Text != "line 1: x\nline 2: y" {
		t.Errorf("lint errors = %q", got.Text)
	}
	if got := r.call("skill", map[string]any{"name": "standards-go#errors"}); got.Text != "skill standards-go#errors" {
		t.Errorf("skill = %q", got.Text)
	}
	if got := r.call("skill", map[string]any{"name": "nope"}); got.Text != "error: no such skill" {
		t.Errorf("skill error = %q", got.Text)
	}
}

func TestDiffAndGitReadHideSecretFiles(t *testing.T) {
	patch := "diff --git a/.env b/.env\nnew file mode 100644\n+TOKEN=hunter2\ndiff --git a/a.go b/a.go\n+func A() {}\n"
	r := newRig(t, func(e *Env) { e.Git = fakeGit{diff: patch, read: patch} })
	for _, got := range []Reply{r.call("diff", map[string]any{}), r.call("git_read", map[string]any{"command": "show"})} {
		if strings.Contains(got.Text, "hunter2") || !strings.Contains(got.Text, "diff --git a/.env b/.env") ||
			!strings.Contains(got.Text, secretNote) || !strings.Contains(got.Text, "+func A() {}") {
			t.Fatalf("reply = %q", got.Text)
		}
	}
}

func TestOutsideToolsAreGuardedAndRecorded(t *testing.T) {
	f := newFixture(t)
	env := &Env{Worktree: f.Root}
	var facts []Fact
	env.OnFact = func(x Fact) { facts = append(facts, x) }
	reg := NewRegistry(env)
	reg.AddOutside(llm.ToolDef{Name: "docs.lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, json.RawMessage) (string, string) { return "found", "ok" })
	perms := Permissions{Tools: []string{"read", "docs.lookup"}, Servers: []string{"docs"}}
	b, err := reg.Bind(perms.Tools, newTestGuard(t, f, "reviewer", perms, nil, 3))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Dispatch(context.Background(), "docs.lookup", json.RawMessage(`{"q":"x"}`)); got.Text != "found" {
		t.Fatalf("reply = %q", got.Text)
	}
	if got := b.Dispatch(context.Background(), "docs.other", json.RawMessage(`{}`)); !strings.Contains(got.Text, "G1") {
		t.Fatalf("unlisted outside tool = %q", got.Text)
	}
	if len(facts) != 2 || facts[0].Subtype != "tool.outside" || facts[1].Subtype != "tool.denied" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestCapTextKeepsHeadAndTailWithinTheCap(t *testing.T) {
	s := strings.Repeat("é", 5000)
	got := capText(s, "/x/out/c1.log")
	if len(got) > replyCap || !strings.Contains(got, "bytes cut; full output in /x/out/c1.log") {
		t.Fatalf("len %d", len(got))
	}
	if !strings.HasPrefix(got, "é") || !strings.HasSuffix(got, "é") {
		t.Fatal("cut inside a character")
	}
	if short := capText("short", "x"); short != "short" {
		t.Fatalf("short text changed: %q", short)
	}
}

// toolLines reads tools.jsonl of a session folder.
func toolLines(t *testing.T, dir string) []ToolLine {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []ToolLine
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var l ToolLine
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("bad line %q: %v", ln, err)
		}
		out = append(out, l)
	}
	return out
}
