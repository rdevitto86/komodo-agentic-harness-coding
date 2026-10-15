package cleanup

import (
	"context"
	"errors"
	"fmt"
)

// Discard drops one group's resources for good, including unpublished work; the worktree and its files are deleted unless KeepWorktree is set.
func (h *Handler) Discard(ctx context.Context, group string, o DiscardOptions) (Report, error) {
	rep := Report{Group: group}
	if h.Task == nil {
		return rep, errors.New("no task manager to close the group")
	}
	path, err := child(h.Roots.Worktrees, group)
	if err != nil {
		return rep, err
	}
	unlock, err := h.lockGroup(ctx, group)
	if err != nil {
		return rep, fmt.Errorf("lock group %s: %w", group, err)
	}
	defer unlock()
	l, have, live, err := h.holder(ctx, group)
	if err != nil {
		return rep, fmt.Errorf("read lease of %s: %w", group, err)
	}
	if live {
		return rep, fmt.Errorf("group %s: %w", group, ErrLeaseHeld)
	}
	r := &run{h: h, ctx: ctx, group: group, rep: &rep, guard: true}
	r.container()
	r.checkouts()
	gone := false
	if o.KeepWorktree {
		r.kept(KindWorktree, path, "kept on request")
	} else {
		gone = r.worktree(h.Roots.Worktrees, path, true)
	}
	if gone {
		h.discardBranch(r, l.Branch, have)
	} else {
		r.keepBranch(l.Branch)
	}
	if r.stopped {
		return rep, fmt.Errorf("group %s: %w", group, ErrLeaseHeld)
	}
	if err := h.Task.CloseTaskGroup(ctx); err != nil {
		h.escalate(group, fmt.Errorf("close task group: %w", err))
		return rep, fmt.Errorf("close task group %s: %w", group, err)
	}
	h.say(ctx, group, "group.discarded", DiscardedFact{Reason: o.Reason})
	return rep, nil
}

// discardBranch deletes the group's branch whether or not the remote holds its tip; with no lease record it cannot name the branch.
func (h *Handler) discardBranch(r *run, branch string, have bool) {
	if !have || branch == "" {
		r.kept(KindBranch, "", "no lease record names the group's branch")
		return
	}
	if !r.allowed(branch) || !r.proceed() {
		return
	}
	tip, ok, err := h.Git.BranchTip(r.ctx, h.Clone, branch)
	if err != nil {
		r.fail(KindBranch, branch, err)
		return
	}
	if !ok {
		return
	}
	if err := h.Git.DiscardBranch(r.ctx, h.Clone, branch, tip); err != nil {
		r.fail(KindBranch, branch, err)
		return
	}
	r.removed(KindBranch, branch)
}
