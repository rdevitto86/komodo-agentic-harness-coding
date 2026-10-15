package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// fenceFileName holds, per repository, the highest fence each group ever had; no group id starts with a dot.
const fenceFileName = ".fences.json"

// FenceKeeper is implemented by a registry that remembers the highest fence each group ever had.
// Callers hold the repository's lock around HighFence and RaiseFence.
type FenceKeeper interface {
	HighFence(ctx context.Context, repo, group string) (Fence, error)
	RaiseFence(ctx context.Context, repo, group string, f Fence) error
}

// readFences reads a repository's high-water file; an absent file is an empty map and a broken one is an error.
func (r *FileRegistry) readFences(repo string) (map[string]Fence, string, error) {
	dir, err := r.repoDir(repo)
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, fenceFileName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Fence{}, path, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read fence file: %w", err)
	}
	m := map[string]Fence{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", fmt.Errorf("decode fence file: %w", err)
	}
	return m, path, nil
}

// HighFence returns the highest fence the group ever had, or 0 for a group that never had a lease.
func (r *FileRegistry) HighFence(ctx context.Context, repo, group string) (Fence, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m, _, err := r.readFences(repo)
	return m[group], err
}

// RaiseFence records f as the group's highest fence when it is above the one recorded.
func (r *FileRegistry) RaiseFence(ctx context.Context, repo, group string, f Fence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m, path, err := r.readFences(repo)
	if err != nil {
		return err
	}
	if m[group] >= f {
		return nil
	}
	m[group] = f
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fence file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lease folder: %w", err)
	}
	return core.WriteFileAtomic(path, append(b, '\n'), 0o644)
}
