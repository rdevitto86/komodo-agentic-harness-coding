package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrLockWait reports that another holder kept the lock for the whole wait.
var ErrLockWait = errors.New("lock is held by another process")

// FileLock takes an exclusive operating-system lock on the file at path, creating the file when it is absent. It retries
// until the context ends or wait passes (then ErrLockWait); the kernel frees
// the lock when its holder dies.
func FileLock(ctx context.Context, path string, wait time.Duration) (unlock func() error, err error) {
	// lockPoll is the pause between attempts to take a held lock.
	const lockPoll = 10 * time.Millisecond
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	deadline := time.Now().Add(wait)

	for {
		var busy bool

		busy, err = tryLock(f, false)
		if err == nil && !busy {
			return func() error { return releaseLock(f) }, nil
		}
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if cerr := ctx.Err(); cerr != nil {
			_ = f.Close()
			return nil, cerr
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrLockWait
		}

		select {
		case <-ctx.Done():
		case <-time.After(lockPoll):
		}
	}
}

// LockHeld reports whether another holder has the lock on path, opening the file read-only and never creating it. A missing
// file is not held; any other failure counts as held, so a caller errs
// toward waiting.
func LockHeld(path string) bool {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}

	defer func() { _ = f.Close() }()

	busy, err := tryLock(f, true)
	if err != nil || busy {
		return true
	}

	_ = unlockFile(f)
	return false
}

// releaseLock unlocks and closes the lock file; closing alone would free the lock too.
func releaseLock(f *os.File) error {
	uerr := unlockFile(f)
	cerr := f.Close()
	return errors.Join(uerr, cerr)
}
