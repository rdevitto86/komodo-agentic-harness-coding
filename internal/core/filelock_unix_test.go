//go:build unix

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A stopped holder keeps the lock however old the lock file looks.
func TestFileLockStoppedHolderNeverLosesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	cmd, release := startHolder(t, path)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if _, err := FileLock(context.Background(), path, 600*time.Millisecond); !errors.Is(err, ErrLockWait) {
		t.Fatalf("stopped holder: want ErrLockWait, got %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child exit: %v", err)
	}
	unlock, err := FileLock(context.Background(), path, 5*time.Second)
	if err != nil {
		t.Fatalf("after the holder released: %v", err)
	}
	_ = unlock()
}
