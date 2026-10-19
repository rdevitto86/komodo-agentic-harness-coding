//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// fakeClaudeScript stands in for the claude program: it records its arguments and replays out.jsonl.
const fakeClaudeScript = `#!/bin/sh
d="$FAKE_DIR"
case "$1" in
--version) cat "$d/version"; exit 0 ;;
auth) cat "$d/auth.json"; exit 0 ;;
esac
printf '%s\n' "$@" > "$d/args.txt"
IFS= read -r first
printf '%s\n' "$first" >> "$d/stdin.txt"
while IFS= read -r line; do
  case "$line" in
    "#sleep "*) sleep "${line#\#sleep }" ;;
    *) printf '%s\n' "$line" ;;
  esac
done < "$d/out.jsonl"
while IFS= read -r l; do printf '%s\n' "$l" >> "$d/stdin.txt"; done
`

// uuidShape matches the session identifier the claude program accepts.
var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// plannerFixture is a repository kit whose claude link runs the fake program.
type plannerFixture struct {
	home string
	fake string
	kit  *repoKit
}

// newPlannerFixture makes a git repository, a fake claude on PATH that prints out, and a kit over them.
func newPlannerFixture(t *testing.T, out ...string) *plannerFixture {
	t.Helper()

	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root

		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := t.TempDir()
	bin := filepath.Join(fake, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"bin/claude": fakeClaudeScript,
		"version":    "2.1.295 (Claude Code)\n",
		"auth.json":  `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`,
		"out.jsonl":  strings.Join(out, "\n") + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", fake)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")

	home := t.TempDir()

	s, err := newStack(home, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx

	t.Cleanup(func() { cancel(); s.close() })

	k, err := s.kitFor(root, "", "repo")
	if err != nil {
		t.Fatal(err)
	}

	return &plannerFixture{home: home, fake: fake, kit: k}
}

// planFolder returns the planner session folder, waiting until the session wrote its tool config, brief and system text.
// The tool config is written when the session starts and the brief on its first send, so mcp.json alone is too early.
func (f *plannerFixture) planFolder(t *testing.T) string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		m, _ := filepath.Glob(filepath.Join(f.home, "sessions", "planner-*", "mcp.json"))
		if len(m) == 1 && written(filepath.Dir(m[0]), "brief.md", "system.md") {
			return filepath.Dir(m[0])
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("the planner session never wrote mcp.json")

	return ""
}

func TestPlannerSessionGetsItsBriefToolConfigAndLiveToolRoute(t *testing.T) {
	const goal = "Add lease revocation to the engine"

	f := newPlannerFixture(
		t,
		`{"type":"system","subtype":"init","apiKeySource":"none","session_id":"s1"}`,
		"#sleep 2",
		`{"type":"result","subtype":"success","structured_output":{"files":[]},"total_cost_usd":0.1}`,
	)

	done := make(chan error, 1)

	go func() {
		_, err := f.kit.intake.Plan(context.Background(), intake.PlanRequest{Goal: goal, Root: f.kit.root})
		done <- err
	}()

	dir := f.planFolder(t)
	id := strings.TrimPrefix(filepath.Base(dir), "planner-")

	brief, err := os.ReadFile(filepath.Join(dir, "brief.md"))
	if err != nil {
		t.Fatalf("the planner session has no brief.md: %v", err)
	}
	if !strings.Contains(string(brief), goal) || !strings.Contains(string(brief), "You are the planner") ||
		strings.Contains(string(brief), "planner tools") {
		t.Fatalf("brief.md is not the Context Manager's planner brief:\n%s", brief)
	}
	if sys, err := os.ReadFile(filepath.Join(dir, "system.md")); err != nil || len(sys) == 0 {
		t.Fatalf("system.md: %q %v", sys, err)
	}

	var cfg struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}

	raw, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || json.Unmarshal(raw, &cfg) != nil || len(cfg.Servers) != 1 || !strings.Contains(string(raw), id) {
		t.Fatalf("mcp.json does not name the planner session %s: %s %v", id, raw, err)
	}

	defs, ok := f.kit.s.tools.Definitions(id)
	if !ok {
		t.Fatalf("no tool route for the planner session %s", id)
	}

	names := []string{}
	for _, d := range defs {
		names = append(names, d.Name)
	}

	if strings.Join(names, ",") != "read,search,lint_backlog" {
		t.Fatalf("routed tools = %v", names)
	}

	args, _ := json.Marshal(map[string]string{"draft": "not json"})

	text, ok := f.kit.s.tools.Call(context.Background(), id, "lint_backlog", args)
	if !ok || !strings.Contains(text, "not a JSON task list") {
		t.Fatalf("lint_backlog through the route = %q (found %v)", text, ok)
	}

	args, _ = json.Marshal(map[string]string{"path": "go.mod"})

	text, ok = f.kit.s.tools.Call(context.Background(), id, "read", args)
	if !ok || !strings.Contains(text, "module x") {
		t.Fatalf("read through the route = %q (found %v)", text, ok)
	}

	if err := <-done; err == nil {
		t.Fatal("a planner that filed no files ended the plan without an error")
	}
	if _, ok := f.kit.s.tools.Definitions(id); ok {
		t.Fatal("the planner's tool route outlived the session")
	}

	argv, _ := os.ReadFile(filepath.Join(f.fake, "args.txt"))
	if !strings.Contains(string(argv), "--mcp-config\n"+filepath.Join(dir, "mcp.json")) {
		t.Fatalf("claude was not given the session's mcp.json:\n%s", argv)
	}
	if !uuidShape.MatchString(id) {
		t.Fatalf("session id %q is not a UUID, which claude --session-id requires", id)
	}
}

// written reports whether every named file in dir exists and is not empty.
func written(dir string, names ...string) bool {
	for _, n := range names {
		if fi, err := os.Stat(filepath.Join(dir, n)); err != nil || fi.Size() == 0 {
			return false
		}
	}

	return true
}
