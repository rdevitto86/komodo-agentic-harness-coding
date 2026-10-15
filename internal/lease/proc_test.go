package lease

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessStartAndCheck(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	start, err := ProcessStart(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ProcessStart: %v", err)
	}
	if d := time.Since(start); d < -2*time.Second || d > time.Minute {
		t.Fatalf("start %v is not near now", start)
	}
	if !OSProcessCheck(cmd.Process.Pid, start) {
		t.Fatal("a running process with its own start time must be alive")
	}
	if OSProcessCheck(cmd.Process.Pid, start.Add(-time.Hour)) {
		t.Fatal("the same pid with another start time must be dead")
	}
	if !OSProcessCheck(os.Getpid(), mustStart(t, os.Getpid())) {
		t.Fatal("this process must be alive")
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if OSProcessCheck(cmd.Process.Pid, start) {
		t.Fatal("a killed process must be dead")
	}
	if OSProcessCheck(0, start) || OSProcessCheck(-5, start) {
		t.Fatal("a non-positive pid is never alive")
	}
}

// mustStart returns the start time of a process or fails the test.
func mustStart(t *testing.T, pid int) time.Time {
	t.Helper()
	s, err := ProcessStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
