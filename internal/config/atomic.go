package config

import (
	"crypto/rand"
	"encoding/hex"
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
// The file holds the writer id; a lock older than lockStale is taken over by renaming it away first.
func (m *Manager) lock() (func(), error) {
	if err := os.MkdirAll(m.stateDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create state folder: %w", err)
	}
	id := newWriterID()
	start := time.Now()
	for {
		f, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(id)
			if cerr := f.Close(); werr != nil || cerr != nil {
				_ = os.Remove(m.lockPath())
				return nil, fmt.Errorf("write settings.lock: %w", errors.Join(werr, cerr))
			}
			return func() { m.unlock(id) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("take settings.lock: %w", err)
		}
		if m.clearStale() {
			continue
		}
		if time.Since(start) >= lockWait {
			return nil, ErrLocked
		}
		time.Sleep(lockPoll)
	}
}

// newWriterID returns the process id and a random token, which name one lock holder.
func newWriterID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(b[:])
}

// clearStale moves a lock older than lockStale to a unique name and deletes it; it reports whether it did.
// A lock found fresh after the move was replaced meanwhile, so it goes back under its own name.
func (m *Manager) clearStale() bool {
	st, err := os.Stat(m.lockPath())
	if err != nil || time.Since(st.ModTime()) <= lockStale {
		return false
	}
	moved := m.lockPath() + ".stale-" + newWriterID()
	if os.Rename(m.lockPath(), moved) != nil {
		return false
	}
	if mt, err := os.Stat(moved); err == nil && time.Since(mt.ModTime()) <= lockStale {
		_ = os.Link(moved, m.lockPath())
		_ = os.Remove(moved)
		return false
	}
	_ = os.Remove(moved)
	return true
}

// unlock removes settings.lock only while it still holds the given writer id.
func (m *Manager) unlock(id string) {
	if b, err := os.ReadFile(m.lockPath()); err == nil && string(b) == id {
		_ = os.Remove(m.lockPath())
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
