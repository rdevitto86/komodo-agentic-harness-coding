package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Sweep removes the remains of runs that ended without cleaning up.
func (h *Handler) Sweep(ctx context.Context) (Report, error) {
	rep, err := h.sweep(ctx, false)
	if err == nil {
		h.say(ctx, "", "cleanup.swept", rep)
	}
	return rep, err
}

// Preview lists what Sweep would remove and removes nothing; a worktree that holds unpublished files shows only when Sweep runs.
func (h *Handler) Preview(ctx context.Context) (Report, error) { return h.sweep(ctx, true) }

// sweep visits every group folder under the worktree root and cleans the ones no live run holds.
func (h *Handler) sweep(ctx context.Context, dry bool) (Report, error) {
	rep := Report{}
	groups, err := h.groupFolders()
	if err != nil {
		return rep, err
	}
	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		h.sweepGroup(ctx, &rep, g, dry)
	}
	return rep, nil
}

// groupFolders returns the names of the group folders in the worktree root; stray files and odd names are ignored.
func (h *Handler) groupFolders() ([]string, error) {
	if h.Roots.Worktrees == "" {
		return nil, errors.New("no worktree folder to sweep")
	}
	ents, err := os.ReadDir(h.Roots.Worktrees)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsDir() && validName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// sweepGroup cleans one group's remains, or lists them when dry; a group a live run holds is kept untouched.
func (h *Handler) sweepGroup(ctx context.Context, rep *Report, group string, dry bool) {
	path := filepath.Join(h.Roots.Worktrees, group)
	l, have, live, err := h.holder(ctx, group)
	if err != nil {
		rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: err.Error()})
		return
	}
	if live {
		rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: ErrLeaseHeld.Error()})
		return
	}
	r := &run{h: h, ctx: ctx, group: group, rep: rep, dry: dry}
	r.container()
	r.checkouts()
	r.worktree(h.Roots.Worktrees, path, false)
	switch {
	case have && l.Branch != "":
		r.redundantBranch(l.Branch)
	default:
		r.kept(KindBranch, "", "no lease record names the group's branch")
	}
}
