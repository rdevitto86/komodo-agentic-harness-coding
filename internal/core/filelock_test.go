package core

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// lockChildEnv names the lock file a re-executed test binary holds until it is killed or its input closes.
const lockChildEnv = "KOMODO_TEST_LOCK_CHILD"

// TestMain runs the lock-holding child when the environment asks for one, and the tests otherwise.
func TestMain(m *testing.M) {
	if path := os.Getenv(lockChildEnv); path != "" {
		os.Exit(holdLockChild(path))
	}
	os.Exit(m.Run())
}

func TestFileLockExcludesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := FileLock(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FileLock(context.Background(), path, 50*time.Millisecond); !errors.Is(err, ErrLockWait) {
		t.Fatalf("second lock: want ErrLockWait, got %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lock file should stay on disk: %v", err)
	}
	again, err := FileLock(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	_ = again()
}

func TestFileLockContextEndsWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, _ := FileLock(context.Background(), path, time.Second)
	defer func() { _ = unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := FileLock(ctx, path, 10*time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the context did not end the wait")
	}
}

func TestFileLockCancelledContextTakesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FileLock(ctx, filepath.Join(t.TempDir(), "x.lock"), time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestFileLockMissingFolderFails(t *testing.T) {
	if _, err := FileLock(context.Background(), filepath.Join(t.TempDir(), "no", "x.lock"), time.Second); err == nil {
		t.Fatal("want an error for a missing folder")
	}
}

func TestFileLockSerialisesCriticalSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	var inside, worst int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := FileLock(context.Background(), path, 10*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			inside++
			worst = max(worst, inside)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			_ = unlock()
		}()
	}
	wg.Wait()
	if worst != 1 {
		t.Fatalf("%d holders at once", worst)
	}
}

// A killed holder frees the lock at once, with no wait for any age.
func TestFileLockKilledHolderFreesAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	cmd, _ := startHolder(t, path)
	if _, err := FileLock(context.Background(), path, 50*time.Millisecond); !errors.Is(err, ErrLockWait) {
		t.Fatalf("while the child lives: want ErrLockWait, got %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	start := time.Now()
	unlock, err := FileLock(context.Background(), path, 5*time.Second)
	if err != nil {
		t.Fatalf("after the kill: %v", err)
	}
	_ = unlock()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("lock took %v after the holder died", d)
	}
}

// LockHeld sees a lock through a read-only open, reports a missing file as free and any other open failure as held.
func TestLockHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")
	if LockHeld(path) {
		t.Fatal("a missing lock file is not held")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("LockHeld must not create the file")
	}
	unlock, err := FileLock(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	if !LockHeld(path) {
		t.Fatal("a held lock in a read-only folder must show as held")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if LockHeld(path) {
		t.Fatal("a freed lock must show as free")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 && !LockHeld(path) {
		t.Fatal("a file that cannot be opened must count as held, so callers reread")
	}
}

// holdLockChild locks path, prints "locked", and waits for its standard input to close.
func holdLockChild(path string) int {
	unlock, err := FileLock(context.Background(), path, time.Second)
	if err != nil {
		println("child lock failed:", err.Error())
		return 2
	}
	println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	_ = unlock()
	return 0
}

// startHolder starts a child process that holds the lock on path and returns it with a function that releases it.
func startHolder(t *testing.T, path string) (*exec.Cmd, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockChildEnv+"="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, _ := bufio.NewReader(errOut).ReadString('\n')
	if line != "locked\n" {
		t.Fatalf("child did not take the lock: %q", line)
	}
	return cmd, func() { _ = in.Close() }
}
