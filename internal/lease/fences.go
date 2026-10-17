package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// FenceKeeper is implemented by a registry that remembers the highest fence each group ever had.
// Callers hold the repository's lock around HighFence and RaiseFence.
type FenceKeeper interface {
	HighFence(ctx context.Context, repo, group string) (Fence, error)
	RaiseFence(ctx context.Context, repo, group string, f Fence) error
}

// floorKey names the entry that holds the rebuilt file's floor; no group id starts with a dot.
const floorKey = ".floor"

// HighFence returns the highest fence the group ever had, or 0 for a group that never had a lease.
// After a rebuild it is at least the rebuilt file's floor.
func (r *FileRegistry) HighFence(ctx context.Context, repo, group string) (Fence, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	m, _, err := r.readFences(ctx, repo)

	return max(m[group], m[floorKey]), err
}

// RaiseFence records f as the group's highest fence when it is above the one recorded.
func (r *FileRegistry) RaiseFence(ctx context.Context, repo, group string, f Fence) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m, path, err := r.readFences(ctx, repo)
	if err != nil {
		return err
	}

	if max(m[group], m[floorKey]) >= f {
		return nil
	}

	m[group] = f

	return r.writeFences(path, m)
}

// readFences reads a repository's high-water file; an absent file is an empty map.
// A damaged file is set aside as .fences.json.bad-<unix>, reported once and rebuilt with a floor
// above every fence handed out.
func (r *FileRegistry) readFences(
	ctx context.Context,
	repo string,
) (map[string]Fence, string, error) {
	// fenceFileName holds, per repository, the highest fence each group ever had; no group id
	// starts with a dot.
	const fenceFileName = ".fences.json"

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

	if jerr := json.Unmarshal(b, &m); jerr != nil {
		m, err = r.rebuildFences(ctx, repo, path, jerr)
		if err != nil {
			return nil, "", err
		}
	}

	return m, path, nil
}

// rebuildFences sets a damaged fence file aside and writes a new one whose floor is the larger of
// the current Unix seconds and the highest fence in any record of the repository.
func (r *FileRegistry) rebuildFences(
	ctx context.Context,
	repo, path string,
	cause error,
) (map[string]Fence, error) {
	floor := Fence(time.Now().Unix())

	recs, err := r.List(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("rebuild fence file: %w", err)
	}

	for _, l := range recs {
		floor = max(floor, l.Fence)
	}

	aside := path + ".bad-" + strconv.FormatInt(time.Now().Unix(), 10)

	if err := os.Rename(path, aside); err != nil {
		return nil, fmt.Errorf("set aside fence file: %w", err)
	}

	m := map[string]Fence{floorKey: floor}

	if err := r.writeFences(path, m); err != nil {
		return nil, err
	}

	if r.OnRepair != nil {
		r.OnRepair(aside, cause)
	}

	return m, nil
}

// writeFences replaces the high-water file with m.
func (r *FileRegistry) writeFences(path string, m map[string]Fence) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fence file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lease folder: %w", err)
	}

	return core.WriteFileAtomic(path, append(b, '\n'), 0o644)
}
