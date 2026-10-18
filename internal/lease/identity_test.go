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

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

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
	noOSMachineID(t)
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
	noOSMachineID(t)
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

// The operating system's id wins over the home folder, and the home folder then holds no id file.
func TestMachineIDComesFromTheOperatingSystem(t *testing.T) {
	old := osMachineID
	osMachineID = func() string { return "11111111-2222-3333-4444-555555555555" }
	defer func() { osMachineID = old }()
	home := t.TempDir()
	id, err := machineID(home)
	if err != nil || id != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("machineID = %q %v", id, err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "machine-id")); !os.IsNotExist(err) {
		t.Fatalf("the home folder file should not be made when the system has an id: %v", err)
	}
	other, _ := machineID(t.TempDir())
	if other != id {
		t.Fatal("two home folders on one machine must see one id")
	}
}

// The system id is read from the Linux files in order and from the macOS registry dump.
func TestReadOSMachineIDSources(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none")
	first := filepath.Join(dir, "machine-id")
	second := filepath.Join(dir, "dbus-id")
	if err := os.WriteFile(first, []byte("not an id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("0123456789abcdef0123456789ABCDEF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstFileID([]string{missing, first, second}); got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("firstFileID = %q", got)
	}
	if got := firstFileID([]string{missing}); got != "" {
		t.Fatalf("firstFileID of nothing = %q", got)
	}
	dump := []byte(
		"+-o Root  <class IORegistryEntry>\n    {\n      \"IOPlatformUUID\" = \"9ABCDEF0-1234-5678-9ABC-DEF012345678\"\n  " +
			"    \"IOPlatformSerialNumber\" = \"X\"\n    }\n",
	)
	if got := parseIOPlatformUUID(dump); got != "9abcdef0-1234-5678-9abc-def012345678" {
		t.Fatalf("parseIOPlatformUUID = %q", got)
	}
	if got := parseIOPlatformUUID([]byte("nothing here")); got != "" {
		t.Fatalf("parseIOPlatformUUID of nothing = %q", got)
	}
}

// A holder with no recorded start time is judged by its last renewal, never by its process id alone.
func TestZeroStartUsesRenewalAge(t *testing.T) {
	now := testStart.Add(10 * time.Hour)
	alive := func(int, time.Time) bool { return true }
	l := Lease{State: StateHeld, Host: "m1", PID: 7}
	l.RenewedAt = now.Add(-LeaseExpiry - time.Minute)
	if l.Live(now, "m1", alive) {
		t.Fatal("an unknown start with a renewal past LeaseExpiry must be dead, whatever process holds the pid")
	}
	l.RenewedAt = now.Add(-time.Hour)
	if !l.Live(now, "m1", alive) {
		t.Fatal("an unknown start with a young renewal is live")
	}
}

// A damaged fence file is set aside and rebuilt above every fence handed out before, and reported once.
func TestDamagedFenceFileIsRebuiltAboveEveryFence(t *testing.T) {
	e := newEnv(t)
	m := e.manager(100)
	l1, err := m.Grant(bg, req("run-a", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke(bg, testRepo, "g1", RevokeStop); err != nil {
		t.Fatal(err)
	}
	dir, _ := e.reg.repoDir(testRepo)
	fences := filepath.Join(dir, ".fences.json")
	if err := os.WriteFile(fences, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	var reports []string
	e.reg.OnRepair = func(setAside string, cause error) { reports = append(reports, setAside) }
	before := time.Now().Unix()
	l2, err := m.Grant(bg, req("run-b", "g2"))
	if err != nil {
		t.Fatalf("a damaged fence file must not stop a grant: %v", err)
	}
	if l2.Fence < Fence(before) {
		t.Fatalf("fence %d is below the floor %d", l2.Fence, before)
	}
	if bad, _ := filepath.Glob(fences + ".bad-*"); len(bad) != 1 || len(reports) != 1 || reports[0] != bad[0] {
		t.Fatalf("set aside %v, reported %v", bad, reports)
	}
	l3, err := m.Grant(bg, req("run-c", "g1"))
	if err != nil || l3.Fence <= l1.Fence+1 || l3.Fence < Fence(before) {
		t.Fatalf("g1 must start above every earlier fence: %+v %v", l3, err)
	}
	if len(reports) != 1 {
		t.Fatalf("the repair was reported %d times", len(reports))
	}
}

// A cancelled or expiring context ends a grant that waits behind a holder, in this process or another.
func TestGrantWaitHonoursContext(t *testing.T) {
	e := newEnv(t)
	e.reg = NewFileRegistry(e.root, 30*time.Second)
	m := e.manager(100)
	hold, err := m.lockRepo(bg, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, time.Second)
	start := time.Now()
	_, err = m.Grant(ctx, req("run-a", "g1"))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("in-process holder: want the context error at once, got %v after %v", err, time.Since(start))
	}
	hold()

	other := e.manager(200)
	dir, _ := e.reg.repoDir(testRepo)
	unlock, err := core.FileLock(bg, filepath.Join(dir, repoLockName), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	ctx, cancel = context.WithTimeout(bg, time.Second)
	defer cancel()
	start = time.Now()
	_, err = other.Grant(ctx, req("run-b", "g2"))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("other-process holder: want the context error, got %v after %v", err, time.Since(start))
	}
}

// exitErr returns the error of a shell that wrote stderr and exited with code, or was killed when code is -1.
func exitErr(t *testing.T, stderr string, code int) error {
	t.Helper()
	script := fmt.Sprintf("echo %q >&2; exit %d", stderr, code)
	if stderr == "" {
		script = fmt.Sprintf("exit %d", code)
	}
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

// noOSMachineID makes the operating system give no machine id, so the home-folder file is the source.
func noOSMachineID(t *testing.T) {
	t.Helper()
	old := osMachineID
	osMachineID = func() string { return "" }
	t.Cleanup(func() { osMachineID = old })
}
