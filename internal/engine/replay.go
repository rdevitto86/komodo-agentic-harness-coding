package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// replayTarget resolves what replay names: a live group's label, a group label to the run that last held the group, or a
// run id.
func (e *Engine) replayTarget(ctx context.Context, req Request) (run, group bus.ID, err error) {
	if gs := e.m.byTarget(req.Target); len(gs) > 0 {
		return bus.ID(gs[0].r.id), bus.ID(gs[0].spec.ID), nil
	}

	run, group, ok, err := e.savedGroupRun(ctx, req.Root, req.Target)
	if err != nil || ok {
		return run, group, err
	}

	return bus.ID(req.Target), "", nil
}

// savedGroupRun reads the saved record of the group the label names; ok is false when the label names no group of the plan.
func (e *Engine) savedGroupRun(ctx context.Context, root, label string) (run, group bus.ID, ok bool, err error) {
	if root == "" || e.d.Plans == nil || e.d.States == nil {
		return "", "", false, nil
	}

	specs, err := e.d.Plans.Specs(ctx, root, []string{label})
	if err != nil || len(specs) != 1 {
		return "", "", false, nil
	}

	store, err := e.d.States(commonDirOf(root))
	if err != nil {
		return "", "", false, err
	}

	rec, err := store.Load(ctx, specs[0].ID)
	if errors.Is(err, state.ErrNotFound) {
		return "", "", false, &codeError{CodeUnknownTarget, "no recorded run of " + label}
	}
	if err != nil {
		return "", "", false, err
	}

	return bus.ID(rec.State.Run), bus.ID(specs[0].ID), true, nil
}

// commonDirOf returns the git common directory of the checkout at root; a worktree's .git file names its own git directory.
func commonDirOf(root string) string {
	dir := filepath.Join(root, ".git")
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		if b, err := os.ReadFile(dir); err == nil {
			dir = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
		}
	}

	b, err := os.ReadFile(filepath.Join(dir, "commondir"))
	if err != nil {
		return dir
	}

	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}

	return filepath.Clean(common)
}
