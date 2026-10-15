package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const lockPoll = 10 * time.Millisecond // pause between lock attempts

// errLockLost reports that a writer found its lock replaced before it wrote.
var errLockLost = errors.New("lock was taken over")

// lockHandle is one held lock file and the token that proves it is ours.
type lockHandle struct {
	path  string
	token string
}

// newToken returns the process id and a random suffix, which name one lock holder.
func newToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(b[:])
}

// tokenOf reads the token at the start of a lock file; it is empty when the file is missing or blank.
func tokenOf(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(b)); len(f) > 0 {
		return f[0]
	}
	return ""
}

// acquire takes the lock file by exclusive create, writing our token and the time into it.
// It waits for a fresh lock, replaces a stale one, and gives up when the context ends or the wait passes twice the timeout.
func (r *FileRegistry) acquire(ctx context.Context, path string) (*lockHandle, error) {
	token := newToken()
	deadline := time.Now().Add(2*r.lockTimeout + time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(token + " " + time.Now().UTC().Format(time.RFC3339) + "\n")
			if cerr := f.Close(); werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lock file: %w", errors.Join(werr, cerr))
			}
			return &lockHandle{path: path, token: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("take lock file: %w", err)
		}
		if r.clearStale(path) {
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrLocked
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// generation names one lock file: its token, or its modification time when it holds no token.
func generation(path string, st os.FileInfo) string {
	if t := tokenOf(path); t != "" {
		return t
	}
	return "m" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// clearStale removes a lock older than the timeout and reports whether it did.
// Only the process that creates the claim file named after the stale lock's generation may remove it, so
// two processes never clear one lock twice, and the second never removes a fresh lock the first made room for.
func (r *FileRegistry) clearStale(path string) bool {
	st, err := os.Stat(path)
	if err != nil || time.Since(st.ModTime()) <= r.lockTimeout {
		return false
	}
	gen := generation(path, st)
	claim := path + ".stale-" + gen
	f, err := os.OpenFile(claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// A claim older than the timeout belongs to a taker that died; drop it so a later attempt can claim.
		if cst, serr := os.Stat(claim); serr == nil && time.Since(cst.ModTime()) > r.lockTimeout {
			_ = os.Remove(claim)
		}
		return false
	}
	_ = f.Close()
	defer os.Remove(claim)
	return removeIfSame(path, gen, r.lockTimeout)
}

// removeIfSame deletes the lock at path while it is still the stale generation gen.
// It moves the file away and checks it is the file it saw, putting any other file back.
func removeIfSame(path, gen string, timeout time.Duration) bool {
	seen, err := os.Stat(path)
	if err != nil || time.Since(seen.ModTime()) <= timeout || generation(path, seen) != gen {
		return false
	}
	moved := path + ".gone-" + newToken()
	if os.Rename(path, moved) != nil {
		return false
	}
	if now, err := os.Stat(moved); err != nil || !os.SameFile(seen, now) {
		_ = os.Link(moved, path)
		_ = os.Remove(moved)
		return false
	}
	_ = os.Remove(moved)
	return true
}

// owned reports whether the lock file still carries our token.
func (h *lockHandle) owned() bool { return tokenOf(h.path) == h.token }

// release deletes the lock file only while it still carries our token.
func (h *lockHandle) release() {
	if h.owned() {
		_ = os.Remove(h.path)
	}
}
