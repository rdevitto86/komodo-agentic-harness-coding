package cleanup

import (
	"context"
	"errors"
	"fmt"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// Leases is the Lease Manager's read side; the Cleanup Handler never writes to it.
type Leases interface {
	Holder(ctx context.Context, repo, group string) (lease.Lease, bool, error)
}

// Git is the part of the git wrapper the Cleanup Handler uses.
// An error that carries a Refused() bool method answering true is a refusal on purpose, not a failure.
type Git interface {
	// RemoveWorktree removes a worktree and refuses when it holds files the tip lacks.
	RemoveWorktree(ctx context.Context, clone, path string) error
	// DeleteBranch deletes a local branch only when it still points at expectTip and that commit is in the history of containedIn.
	DeleteBranch(ctx context.Context, clone, branch, expectTip, containedIn string) error
	// DiscardBranch deletes a local branch that still points at expectTip, whether or not the remote holds that commit; only Discard calls it.
	DiscardBranch(ctx context.Context, clone, branch, expectTip string) error
	// DiscardWorktree removes a worktree and its files even when they hold work the tip lacks; only Discard calls it.
	DiscardWorktree(ctx context.Context, clone, path string) error
	// BranchTip returns the commit a local branch points at; the bool is false when the branch does not exist.
	BranchTip(ctx context.Context, clone, branch string) (tip string, ok bool, err error)
	// Holds reports whether the remote holds tip in the history of the reference contains, or of any of its branches when contains is empty.
	Holds(ctx context.Context, clone, remote, contains, tip string) (ref string, ok bool, err error)
}

// Containers removes a group's Docker container; the sandbox backend that created it implements it, and it succeeds when Docker is absent or the container is gone.
type Containers interface {
	Remove(ctx context.Context, group string) error
}

// TaskGroup is the group's Task Manager.
type TaskGroup interface {
	CloseTaskGroup(ctx context.Context) error // destroys the group's sandbox and ends the group in the Runtime Manager
}

// Groups reads group records; the State Manager implements it.
type Groups interface {
	Unfinished(ctx context.Context) ([]GroupRef, error)
}

// GroupRef names an unfinished group and the binary version its run pinned.
type GroupRef struct {
	Group   string
	Version string
}

// Reporter relays a progress fact to the Runtime Manager, which publishes it as a run-status event.
type Reporter interface {
	Report(ctx context.Context, group string, subtype string, data any) error
}

// Errors is the Error Handler as the Cleanup Handler uses it.
type Errors interface {
	// Report publishes a removal failure that the Cleanup Handler lists under kept.
	Report(src fault.Source, code string, kind fault.Kind, err error)
}

// Handler cleans up after groups.
type Handler struct {
	Lease  Leases     // reads who holds a group; the Lease Manager
	Git    Git        // the git wrapper, through the Cleanup Handler's own small interface
	Boxes  Containers // the group's Docker container
	Task   TaskGroup  // the group's Task Manager
	Groups Groups     // reads group records from the State Manager
	Report Reporter   // relays a progress fact to the Runtime Manager
	Errors Errors     // receives removal failures; nil drops them

	Repo   string                 // repo id the lease registry files the groups under
	Clone  string                 // path of the clone every git call runs in
	Remote string                 // remote whose branches count as published
	Roots  Roots                  // the only folders the handler deletes in
	Live   func(lease.Lease) bool // reports whether a lease still has a working holder; nil counts every held lease as live
}

// Complete cleans up one published group, then closes it; the Commit Handler calls it after the push.
func (h *Handler) Complete(ctx context.Context, p Published) (Report, error) {
	rep := Report{Group: p.Group}
	if h.Task == nil {
		return rep, errors.New("no task manager to close the group")
	}
	if err := h.checkPublished(p); err != nil {
		return rep, err
	}
	h.say(ctx, p.Group, "cleanup.start", StartedFact{Branch: p.Branch, Tip: p.Tip})
	_, held, err := h.Git.Holds(ctx, h.Clone, p.Remote, p.Contains, p.Tip)
	if err != nil {
		return rep, fmt.Errorf("check remote for tip %s: %w", p.Tip, err)
	}
	if !held {
		return rep, ErrNotPublished
	}
	r := &run{h: h, ctx: ctx, group: p.Group, rep: &rep}
	r.container()
	r.checkouts()
	r.worktree(h.Roots.Worktrees, p.Worktree, false)
	r.branch(p.Branch, p.Tip, p.Contains)
	if p.EpicDone {
		h.cleanEpic(r, p)
	}
	if err := h.Task.CloseTaskGroup(ctx); err != nil {
		h.escalate(p.Group, fmt.Errorf("close task group: %w", err))
		return rep, fmt.Errorf("close task group %s: %w", p.Group, err)
	}
	h.say(ctx, p.Group, "cleanup.done", rep)
	return rep, nil
}

// checkPublished refuses a fact whose group or paths lie outside the folders the handler may delete in.
func (h *Handler) checkPublished(p Published) error {
	if !validName(p.Group) {
		return fmt.Errorf("group %q: %w", p.Group, ErrOutsideRoots)
	}
	if err := inside(h.Roots.Worktrees, p.Worktree); err != nil {
		return err
	}
	if p.EpicDone && p.Landing != "" {
		return inside(h.Roots.Landings, p.Landing)
	}
	return nil
}

// cleanEpic removes the epic's landing worktree and its local branch once the epic's pull request is open.
func (h *Handler) cleanEpic(r *run, p Published) {
	if p.Landing != "" {
		r.worktree(h.Roots.Landings, p.Landing, false)
	}
	name, ok := epicBranch(p)
	if !ok {
		r.kept(KindBranch, p.Contains, "reference does not name a branch of the remote")
		return
	}
	tip, have, err := h.Git.BranchTip(r.ctx, h.Clone, name)
	if err != nil {
		r.fail(KindBranch, name, err)
		return
	}
	if have {
		r.branch(name, tip, p.Contains)
	}
}
