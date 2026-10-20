package commit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// Save records a finished task's work as a local commit on the group branch; it never pushes.
func (h *Handler) Save(ctx context.Context, r SaveRequest) (SaveResult, error) {
	if !validBranch(r.Branch) {
		return SaveResult{}, fail(
			KindCriticalRef,
			false,
			fmt.Errorf("group branch %q: %w", r.Branch, ErrCriticalRef),
		)
	}

	l, err := h.owns(ctx, r.Repo, r.Group, r.Run, r.Fence)
	if err != nil {
		return SaveResult{}, err
	}

	h.adopt(l.ClonePath)

	cur, err := h.Git.CurrentBranch(ctx, r.Worktree)
	if err != nil {
		return SaveResult{}, gitFail(err)
	}
	if cur != r.Branch {
		return SaveResult{}, fail(
			KindNotApproved,
			false,
			fmt.Errorf("worktree is on %s, not %s: %w", cur, r.Branch, ErrNotApproved),
		)
	}

	before, _, herr := h.Git.Head(ctx, r.Worktree)
	if herr != nil {
		before = ""
	}

	if _, err := h.Git.Commit(ctx, r.Worktree, r.Label+": "+oneLine(r.Task)); err != nil {
		return SaveResult{}, gitFail(err)
	}

	head, tree, err := h.Git.Head(ctx, r.Worktree)
	if err != nil {
		return SaveResult{}, gitFail(err)
	}

	return SaveResult{Head: head, Tree: tree, Saved: head != before}, nil
}

// adopt tells a Git that learns clones which clone holds a group's worktree.
func (h *Handler) adopt(clone string) {
	if a, ok := h.Git.(interface{ Adopt(clone string) }); ok && clone != "" {
		a.Adopt(clone)
	}
}

// owns reads the group's lease and refuses unless this run holds it at the given fence.
func (h *Handler) owns(
	ctx context.Context,
	repo string,
	group, run bus.ID,
	fence lease.Fence,
) (lease.Lease, error) {
	l, ok, err := h.Leases.Holder(ctx, repo, string(group))
	if err != nil {
		return lease.Lease{}, fail(KindGit, true, fmt.Errorf("read lease: %w", err))
	}
	if !ok || l.State != lease.StateHeld || l.Run != string(run) || l.Fence != fence {
		return lease.Lease{}, fail(KindLeaseLost, false, ErrLeaseLost)
	}
	return l, nil
}

// fail wraps err as a PublishError of the given kind.
func fail(kind string, retryable bool, err error) *PublishError {
	return &PublishError{Kind: kind, Retryable: retryable, Err: err}
}

// gitFail turns an error from the git wrapper into the PublishError kind its table row names.
func gitFail(err error) *PublishError {
	var pe *PublishError

	switch {
	case errors.As(err, &pe):
		return pe
	case errors.Is(err, ErrCriticalRef):
		return fail(KindCriticalRef, false, err)
	case errors.Is(err, ErrConflict):
		return fail(KindMergeConfl, false, err)
	case errors.Is(err, ErrPushRejected):
		return fail(KindPushRejected, false, err)
	case errors.Is(err, ErrBranchMissing):
		return fail(KindBaseMissing, false, err)
	case errors.Is(err, ErrDiverged),
		errors.Is(err, ErrTrailer),
		errors.Is(err, ErrNoGitDir),
		errors.Is(err, ErrForce),
		errors.Is(err, core.ErrEmptyMessage):
		return fail(KindGit, false, err)
	}

	return fail(KindGit, true, err)
}

// oneLine joins a possibly multi-line text into one line for a commit subject.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// validBranch reports whether name is a komodo/ branch with a safe spelling.
func validBranch(name string) bool { return core.ValidBranch(name) }
