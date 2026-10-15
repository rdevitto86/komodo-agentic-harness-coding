package lease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

// exitErr returns the error of a shell that wrote stderr and exited with code, or was killed when code is -1.
func exitErr(t *testing.T, stderr string, code int) error {
	t.Helper()
	script := fmt.Sprintf("echo %q >&2; exit %d", stderr, code)
	if code < 0 {
		script = "kill -9 $$"
	}
	_, err := exec.Command("sh", "-c", script).Output()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("shell did not fail: %v", err)
	}
	return err
}

// stubPS replaces the ps call for one test.
func stubPS(t *testing.T, out string, err error) {
	t.Helper()
	old := runPS
	runPS = func(context.Context, int) ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { runPS = old })
}

// A ps that timed out, was killed or rejected its flags says nothing about the process, so it is unknown and not dead.
func TestPSFailureIsUnknownNotDead(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	cases := map[string]error{
		"timeout":      context.DeadlineExceeded,
		"killed":       exitErr(t, "", -1),
		"bad flags":    exitErr(t, "ps: illegal option -- o", 1),
		"usage exit 2": exitErr(t, "usage: ps [-AaCcEefhjlMmrSTvwXx]", 2),
	}
	for name, perr := range cases {
		t.Run(name, func(t *testing.T) {
			stubPS(t, "", perr)
			if _, err := ProcessStart(pid); !errors.Is(err, errPSUnknown) {
				t.Fatalf("ProcessStart: want errPSUnknown, got %v", err)
			}
			if !OSProcessCheck(pid, time.Now().Add(-time.Hour)) {
				t.Fatal("a pid that answers signal 0 must stay alive when ps cannot say")
			}
			if OSProcessCheck(1<<22+7, time.Now()) {
				t.Fatal("a pid that answers nothing is gone")
			}
		})
	}
}

// ps exiting 1 with no output is how it says the process does not exist.
func TestPSEmptyExitOneMeansNoProcess(t *testing.T) {
	stubPS(t, "", exitErr(t, "", 1))
	if _, err := ProcessStart(4242); !errors.Is(err, errNoProcess) {
		t.Fatalf("want errNoProcess, got %v", err)
	}
}

// With ps unusable, a process's own start is recorded as unknown and not as the time the package loaded.
func TestSelfStartUnknownIsZero(t *testing.T) {
	stubPS(t, "", context.DeadlineExceeded)
	if got := selfStart(); !got.IsZero() {
		t.Fatalf("selfStart = %v, want the zero time", got)
	}
	if !OSProcessCheck(os.Getpid(), time.Time{}) {
		t.Fatal("a zero start only asks whether the process exists")
	}
}

// The machine id is made once, kept in the state folder and shared by every reader.
func TestMachineIDCreatedOnceAndStable(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := machineID(home)
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
			t.Fatalf("ids differ or are malformed: %q vs %q", id, ids[0])
		}
	}
	b, err := os.ReadFile(filepath.Join(home, "state", "machine-id"))
	if err != nil || string(b) != ids[0]+"\n" {
		t.Fatalf("machine-id file: %q %v", b, err)
	}
	other, _ := machineID(t.TempDir())
	if other == ids[0] {
		t.Fatal("two homes drew the same id")
	}
}

// A lease records the machine id, so a changed network host name never splits liveness.
func TestLeaseRecordsMachineID(t *testing.T) {
	home := t.TempDir()
	m, err := New(home, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := machineID(home)
	l, err := m.Grant(bg, req("run-a", "g1"))
	if err != nil || l.Host != want {
		t.Fatalf("lease host %q, want the machine id %q (%v)", l.Host, want, err)
	}
	host, _ := os.Hostname()
	if l.Host == host {
		t.Fatal("the lease recorded the host name")
	}
}

// A lease needs a home folder for its machine id even over a given registry.
func TestNewNeedsHome(t *testing.T) {
	if _, err := New("", nil, NewFileRegistry(t.TempDir(), 0), nil); err == nil {
		t.Fatal("want an error for an empty home")
	}
}
