package lease

import (
	"context"
	"errors"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// lock takes the operating-system lock on a lock file, waiting at most the registry's lock timeout. It gives ErrLocked when
// the wait passes and the context's error when the context ends first.
func (r *FileRegistry) lock(ctx context.Context, path string) (func(), error) {
	unlock, err := core.FileLock(ctx, path, r.lockTimeout)
	if errors.Is(err, core.ErrLockWait) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = unlock() }, nil
}
