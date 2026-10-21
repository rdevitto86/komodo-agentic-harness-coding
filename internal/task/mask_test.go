package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandsRunWithTheSecretFilesMasked(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if err := os.WriteFile(filepath.Join(r.wt, ".env"), []byte("KEY=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.instance(context.Background(), "qa"); err != nil {
		t.Fatal(err)
	}
	if exit, _, err := r.m.runner("qa")(context.Background(), "test"); err != nil || exit != 0 {
		t.Fatalf("run: %d, %v", exit, err)
	}
	r.be.mu.Lock()
	defer r.be.mu.Unlock()
	if len(r.be.masked) != 1 || filepath.Base(r.be.masked[0]) != ".env" {
		t.Fatalf("masked %v", r.be.masked)
	}
}

// A logged run replays as its argument vector: a pipe or a command substitution in an argument is text, never shell.
func TestLoggedRunReplaysAsAnArgvWithoutAShell(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if _, err := r.m.instance(context.Background(), "qa"); err != nil {
		t.Fatal(err)
	}

	run := r.m.runner("qa")
	for _, arg := range []string{"TestA|TestB", "x; touch pwned", "$(touch pwned)"} {
		exit, out, err := run(context.Background(), argvForm([]string{"printf", "[%s]", arg}))
		if err != nil || exit != 0 || out != "["+arg+"]" {
			t.Errorf("arg %q: exit %d out %q err %v", arg, exit, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.wt, "pwned")); err == nil {
		t.Error("an argument was run as shell")
	}
}
