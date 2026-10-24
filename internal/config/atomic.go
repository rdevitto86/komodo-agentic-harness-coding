package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// longest a writer waits for the lock
const lockWait = 2 * time.Second

// ErrLocked reports that another writer held settings.lock for the whole wait.
var ErrLocked = errors.New("another komodo process holds settings.lock")

// stateDir returns the folder of the guard files, creating it when asked.
func (m *Manager) stateDir() string { return filepath.Join(m.home, "state") }

// lockPath returns the path of the writer lock.
func (m *Manager) lockPath() string { return filepath.Join(m.stateDir(), "settings.lock") }

// lock takes the operating-system lock on settings.lock and returns the function that releases it.
func (m *Manager) lock() (func(), error) {
	if err := os.MkdirAll(m.stateDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create state folder: %w", err)
	}

	unlock, err := core.FileLock(context.Background(), m.lockPath(), lockWait)
	if errors.Is(err, core.ErrLockWait) {
		return nil, ErrLocked
	}

	if err != nil {
		return nil, fmt.Errorf("take settings.lock: %w", err)
	}

	return func() { _ = unlock() }, nil
}

// lockHeld reports whether another holder has settings.lock right now; an unreadable lock file
// counts as held.
func (m *Manager) lockHeld() bool { return core.LockHeld(m.lockPath()) }

// writeAtomic replaces a file in the home folder so a reader sees the old or the new bytes.
func (m *Manager) writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create folder for %s: %w", filepath.Base(path), err)
	}
	return core.WriteFileAtomic(path, data, 0o644)
}
