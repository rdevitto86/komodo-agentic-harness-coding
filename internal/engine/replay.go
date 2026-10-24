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

// replayTarget resolves what replay names: a live group of a plan, a group of a plan to the run that last held it, a plan
// id alone, or a run id. A group label is looked up inside its plan.
func (e *Engine) replayTarget(ctx context.Context, req Request) (run, group bus.ID, err error) {
	plan, label := splitTarget(req.Target)
	if label == "" {
		if live := e.m.RunOf(plan); live != "" {
			return bus.ID(live), "", nil
		}

		return bus.ID(req.Target), "", nil
	}

	if gs := e.m.byPlan(plan, label); len(gs) > 0 {
		return bus.ID(gs[0].r.id), bus.ID(gs[0].spec.ID), nil
	}

	run, group, err = e.savedGroupRun(ctx, req, plan, label)
	if err != nil {
		return "", "", err
	}

	return run, group, nil
}

// savedGroupRun reads the saved record of the group a plan id and label name; the plan must hold the label.
func (e *Engine) savedGroupRun(ctx context.Context, req Request, plan, label string) (run, group bus.ID, err error) {
	if req.Root == "" || e.d.Plans == nil || e.d.States == nil {
		return "", "", &codeError{CodeUnknownTarget, "no recorded run of " + label + " in plan " + plan}
	}

	specs, err := e.d.Plans.Specs(ctx, req.Root, plan, []string{label})
	if err != nil || len(specs) != 1 {
		return "", "", &codeError{CodeUnknownTarget, "no group " + label + " in plan " + plan}
	}

	store, err := e.d.States(commonDirOf(req.Root))
	if err != nil {
		return "", "", err
	}

	rec, err := store.Load(ctx, specs[0].ID)
	if errors.Is(err, state.ErrNotFound) {
		return "", "", &codeError{CodeUnknownTarget, "no recorded run of " + label + " in plan " + plan}
	}
	if err != nil {
		return "", "", err
	}

	return bus.ID(rec.State.Run), bus.ID(specs[0].ID), nil
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
