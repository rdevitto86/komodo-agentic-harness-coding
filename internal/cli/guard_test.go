package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// hookJSON builds the PreToolUse payload of a Write call in repo.
func hookJSON(repo, rel string) string {
	return `{"hook_event_name":"PreToolUse","cwd":"` + repo + `","tool_name":"Write","tool_input":{"file_path":"` +
		filepath.Join(repo, rel) + `","content":"x"}}`
}

func TestGuardAllowsAWriteInsideTheRepository(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	h.stdin = hookJSON(repo, "x.go")
	code, out, errOut := h.run("internal", "guard")
	if code != 0 || strings.TrimSpace(out) != "{}" || errOut != "" {
		t.Fatalf("exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
}

func TestGuardRefusesAProtectedPathWithExitTwo(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	h.stdin = hookJSON(repo, ".claude/settings.json")
	code, out, errOut := h.run("internal", "guard")
	if code != 2 || out != "" || !strings.Contains(errOut, "Refused by G3") {
		t.Fatalf("exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
}

func TestGuardFailsClosedWhenTheCheckCrashes(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	h.stdin = hookJSON(repo, "x.go")
	real := guardCheck
	t.Cleanup(func() { guardCheck = real })
	guardCheck = func(string, string, task.HookInput) (task.Verdict, error) { panic("forced crash") }
	code, out, errOut := h.run("internal", "guard")
	if code != 2 || out != "" || !strings.Contains(errOut, "the guard crashed: forced crash") {
		t.Fatalf("crash: exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
	guardCheck = func(string, string, task.HookInput) (task.Verdict, error) {
		return task.Verdict{}, errors.New("forced error")
	}
	h.stdin = hookJSON(repo, "x.go")
	code, out, errOut = h.run("internal", "guard")
	if code != 2 || out != "" || !strings.Contains(errOut, "forced error") {
		t.Fatalf("error: exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
}

func TestGuardFailsClosedOnInputItCannotRead(t *testing.T) {
	for _, in := range []string{"", "{}", "not json", `{"tool_name":"Edit"}`} {
		h := newHarness(t)
		h.stdin = in
		code, out, errOut := h.run("internal", "guard", "--json")
		if code != 2 || out != "" || !strings.Contains(errOut, "komodo guard:") {
			t.Errorf("input %q: exit %d\nstdout=%q\nstderr=%q", in, code, out, errOut)
		}
	}
}

func TestGuardIgnoresATamperedSettingsFile(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	if err := os.WriteFile(filepath.Join(h.home, "settings.json"), []byte(`{"limits.returns": 2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.stdin = hookJSON(repo, "x.go")
	if code, out, errOut := h.run("internal", "guard"); code != 0 || strings.TrimSpace(out) != "{}" || errOut != "" {
		t.Fatalf("exit %d, stdout=%q, stderr=%q", code, out, errOut)
	}
}

func TestToolsVerbWithNothingWiredFailsOnStderr(t *testing.T) {
	h := newHarness(t)
	real := ToolsMain
	t.Cleanup(func() { ToolsMain = real })
	ToolsMain = nil
	code, out, errOut := h.run("internal", "tools", "--agent", "builder", "--session", "s")
	if code != 4 || out != "" || !strings.Contains(errOut, "not built yet") {
		t.Fatalf("exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
}

func TestToolsVerbPassesItsFlagsAndStreams(t *testing.T) {
	h := newHarness(t)
	h.stdin = "in-line\n"
	real := ToolsMain
	t.Cleanup(func() { ToolsMain = real })
	ToolsMain = func(args []string, in io.Reader, out io.Writer) error {
		body, _ := io.ReadAll(in)
		_, _ = io.WriteString(out, strings.Join(args, " ")+"|"+string(body))
		return nil
	}
	code, out, errOut := h.run("internal", "tools", "--agent", "builder", "--session", "s-1")
	if code != 0 || out != "--agent builder --session s-1|in-line\n" || errOut != "" {
		t.Fatalf("exit %d\nstdout=%q\nstderr=%q", code, out, errOut)
	}
}

func TestLoginIsTerminalOnlyAndNotBuiltYet(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("login", "--claude")
	if code != 1 || e.Error == nil || e.Error.Code != "no_terminal" {
		t.Fatalf("without a terminal: exit %d, %+v", code, e.Error)
	}
	h.terminal = true
	code, e = h.envelope("login", "--claude")
	if code != 4 || e.Error == nil || e.Error.Code != "internal" || !strings.Contains(e.Error.Reason, "login is not built yet") {
		t.Fatalf("with a terminal: exit %d, %+v", code, e.Error)
	}
	if cmd := findCommand("login"); cmd == nil || cmd.Hidden || cmd.Tier != TierTerminal {
		t.Fatalf("login row = %+v", cmd)
	}
}
