//go:build unix

package task

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fix 5: processes a command left behind are killed when the command ends.
func TestRunKillsLeftoverBackgroundProcesses(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := newRig(t, func(e *Env) {
		e.Commands["build"] = []string{"sh", "-c", "sleep 60 & echo $! > " + pidFile + "; echo done"}
	})
	t0 := time.Now()
	got := r.call("run", map[string]any{"class": "build"})
	if got.Text != "done\n" || time.Since(t0) > 3*time.Second {
		t.Fatalf("after %s: %q", time.Since(t0), got.Text)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the background process %d is still running", pid)
	}
}
