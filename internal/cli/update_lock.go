package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// errLockHeld reports that another update or install holds update.lock.
var errLockHeld = errors.New("another komodo update is running")

// lockStale reports whether a lock last touched at modified is old enough to take over.
func lockStale(modified, now time.Time) bool { return now.Sub(modified) > lockStaleAfter }

// claimLock creates update.lock exclusively and returns the function that releases it.
// A lock younger than lockStaleAfter means errLockHeld; an older one is taken over by one claimer at a time.
func claimLock(home string, now func() time.Time) (func(), error) {
	path := filepath.Join(home, "state", "update.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the state folder: %w", err)
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("make a lock token: %w", err)
	}
	token := fmt.Sprintf("%d %s\n", os.Getpid(), hex.EncodeToString(b[:]))
	for range 3 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.WriteString(token)
			if err = errors.Join(err, f.Close()); err != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write update.lock: %w", err)
			}
			return func() { releaseLock(path, token) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("claim update.lock: %w", err)
		}
		fi, serr := os.Stat(path)
		if errors.Is(serr, os.ErrNotExist) {
			continue
		}
		if serr != nil {
			return nil, fmt.Errorf("read update.lock: %w", serr)
		}
		if !lockStale(fi.ModTime(), now()) {
			return nil, errLockHeld
		}
		if err := takeOver(path, now); err != nil {
			return nil, err
		}
	}
	return nil, errLockHeld
}

// takeOver removes a stale lock while holding a second lock, so two claimers never remove each other's fresh lock.
func takeOver(path string, now func() time.Time) error {
	unlock, err := core.FileLock(context.Background(), path+".takeover", 2*time.Second)
	if err != nil {
		return errLockHeld
	}
	defer func() { _ = unlock() }()
	if fi, err := os.Stat(path); err == nil && lockStale(fi.ModTime(), now()) {
		return os.Remove(path)
	}
	return nil
}

// releaseLock removes the lock file only when it still holds this claimer's token.
func releaseLock(path, token string) {
	if b, err := os.ReadFile(path); err == nil && string(b) == token {
		_ = os.Remove(path)
	}
}
