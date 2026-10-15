package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// ErrLocked reports that another writer held settings.lock for the whole wait.
var ErrLocked = errors.New("another komodo process holds settings.lock")

var (
	lockWait   = 2 * time.Second        // longest a writer waits for the lock
	lockStale  = 30 * time.Second       // age after which a lock is replaced
	rereadWait = 100 * time.Millisecond // pause before rereading a mismatch while the lock is held
	lockPoll   = 10 * time.Millisecond  // pause between lock attempts
)

// stateDir returns the folder of the guard files, creating it when asked.
func (m *Manager) stateDir() string { return filepath.Join(m.home, "state") }

// lockPath returns the path of the writer lock.
func (m *Manager) lockPath() string { return filepath.Join(m.stateDir(), "settings.lock") }

// lock takes settings.lock by exclusive create and returns the function that releases it.
// A lock older than lockStale is removed and taken over.
func (m *Manager) lock() (func(), error) {
	if err := os.MkdirAll(m.stateDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create state folder: %w", err)
	}
	start := time.Now()
	for {
		f, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
			_ = f.Close()
			return func() { _ = os.Remove(m.lockPath()) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("take settings.lock: %w", err)
		}
		if st, serr := os.Stat(m.lockPath()); serr == nil && time.Since(st.ModTime()) > lockStale {
			_ = os.Remove(m.lockPath())
			continue
		}
		if time.Since(start) >= lockWait {
			return nil, ErrLocked
		}
		time.Sleep(lockPoll)
	}
}

// lockHeld reports whether a lock file exists.
func (m *Manager) lockHeld() bool {
	_, err := os.Stat(m.lockPath())
	return err == nil
}

// writeAtomic replaces a file in the home folder so a reader sees the old or the new bytes.
func (m *Manager) writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create folder for %s: %w", filepath.Base(path), err)
	}
	return core.WriteFileAtomic(path, data, 0o644)
}
