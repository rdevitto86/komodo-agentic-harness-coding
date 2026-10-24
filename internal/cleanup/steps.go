package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// StartedFact is the data of the cleanup.start fact.
type StartedFact struct {
	Tip string `json:"tip"`
}

// DiscardedFact is the data of the group.discarded fact.
type DiscardedFact struct {
	Reason string `json:"reason"`
}

// run is one call's working state: the group it cleans, the report it fills and whether it only lists.
type run struct {
	h        *Handler
	ctx      context.Context
	group    string
	rep      *Report
	dry      bool
	guard    bool  // re-read the lease before each removal and stop when a live run holds the group
	stopped  bool  // the guard saw a live holder; no further step runs
	noRemote error // why the remote's branches are unknown; branches are then kept
}

// refused reports whether err is a refusal on purpose rather than a failure.
func refused(err error) bool {
	var r interface{ Refused() bool }
	return errors.As(err, &r) && r.Refused()
}

// komodoBranch reports whether name is a branch under komodo/, the only branches the handler may delete.
func komodoBranch(name string) bool {
	return strings.HasPrefix(name, "komodo/") && len(name) > len("komodo/") && !strings.Contains(name, "..")
}

// say relays a progress fact; a failed relay never stops a removal.
func (h *Handler) say(ctx context.Context, group, subtype string, data any) {
	if h.Report != nil {
		_ = h.Report.Report(ctx, group, subtype, data)
	}
}

// escalate sends an error to the Error Handler when one is wired.
func (h *Handler) escalate(group string, err error) {
	if h.Errors != nil {
		h.Errors.Report(fault.Source{
			Component: "cleanup", Group: bus.ID(group),
		}, string(format.Internal), fault.KindInternal, err)
	}
}

// holder reads a group's lease; live is true while a working run holds it.
func (h *Handler) holder(ctx context.Context, group string) (l lease.Lease, have, live bool, err error) {
	if h.Lease == nil {
		return l, false, false, errors.New("no lease reader")
	}
	l, have, err = h.Lease.Holder(ctx, h.Repo, group)
	if err != nil || !have {
		return l, false, false, err
	}
	if h.Live != nil {
		return l, true, h.Live(l), nil
	}
	return l, true, l.State == lease.StateHeld, nil
}

// lockGroup holds the Lease Manager's lock on a group when it offers one; otherwise it returns a no-op release.
func (h *Handler) lockGroup(ctx context.Context, group string) (func(), error) {
	if gl, ok := h.Lease.(GroupLocker); ok {
		return gl.LockGroup(ctx, h.Repo, group)
	}
	return func() {}, nil
}

// proceed reports whether the next removal may run; with the guard on it re-reads the lease first and stops the call for
// good on a live holder.
func (r *run) proceed() bool {
	if r.stopped {
		return false
	}
	if !r.guard || r.dry {
		return true
	}
	_, _, live, err := r.h.holder(r.ctx, r.group)
	if err == nil && !live {
		return true
	}

	r.stopped = true
	reason := ErrLeaseHeld.Error()

	if err != nil {
		reason = err.Error()
	}
	r.kept(KindWorktree, r.group, reason)
	return false
}

// removed lists a removed item.
func (r *run) removed(kind, name string) {
	r.rep.Removed = append(r.rep.Removed, Item{Kind: kind, Name: name})
}

// kept lists a kept item with the reason.
func (r *run) kept(kind, name, reason string) {
	r.rep.Kept = append(r.rep.Kept, Item{Kind: kind, Name: name, Reason: reason})
}

// fail lists a step that did not finish; only a real failure, not a refusal on purpose, goes to the Error Handler.
func (r *run) fail(kind, name string, err error) {
	r.kept(kind, name, err.Error())
	if !refused(err) {
		r.h.escalate(r.group, fmt.Errorf("%s %s: %w", kind, name, err))
	}
}

// container removes the group's Docker container; with no Docker wired the step has nothing to do.
func (r *run) container() {
	if r.h.Boxes == nil || !r.proceed() {
		return
	}
	if !r.dry {
		if err := r.h.Boxes.Remove(r.ctx, r.group); err != nil {
			r.fail(KindContainer, r.group, err)
			return
		}
	}
	r.removed(KindContainer, r.group)
}

// checkouts removes the group's Quality Assurance checkout and session temporary folder.
func (r *run) checkouts() {
	type checkoutKind struct{ kind, root string }
	for _, c := range []checkoutKind{{KindQA, r.h.Roots.QA}, {KindTemp, r.h.Roots.Temp}} {
		if c.root == "" || !r.proceed() {
			continue
		}

		p, err := child(c.root, r.group)
		if err != nil {
			r.fail(c.kind, r.group, err)
			continue
		}

		if r.dry {
			if exists(p) {
				r.removed(c.kind, p)
			}
			continue
		}

		gone, err := removeInside(c.root, p)
		if err != nil {
			r.fail(c.kind, p, err)
		} else if gone {
			r.removed(c.kind, p)
		}
	}
}

// worktree removes a worktree under root; force also drops work the tip lacks. It reports whether the worktree is now gone.
// A link in its place is unlinked and never handed to git, which would follow it.
func (r *run) worktree(root, path string, force bool) bool {
	if err := inside(root, path); err != nil {
		r.fail(KindWorktree, path, err)
		return false
	}
	if !r.proceed() {
		return false
	}

	fi, lerr := os.Lstat(path)
	if lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		if !r.dry {
			if err := os.Remove(path); err != nil {
				r.fail(KindWorktree, path, err)
				return false
			}
		}
		r.removed(KindWorktree, path)
		return true
	}
	if r.dry {
		if lerr == nil {
			r.removed(KindWorktree, path)
		}
		return true
	}

	var err error
	if force {
		err = r.h.Git.DiscardWorktree(r.ctx, r.h.Clone, path)
	} else {
		err = r.h.Git.RemoveWorktree(r.ctx, r.h.Clone, path)
	}
	if err != nil {
		r.fail(KindWorktree, path, err)
		return false
	}
	if lerr == nil {
		r.removed(KindWorktree, path)
	}
	return true
}

// keepBranch lists a branch kept because its worktree is kept.
func (r *run) keepBranch(name string) {
	if name != "" {
		r.kept(KindBranch, name, "kept with its worktree")
	}
}

// allowed reports whether a branch may be deleted; one outside komodo/ is kept and listed.
func (r *run) allowed(name string) bool {
	if komodoBranch(name) {
		return true
	}
	r.kept(KindBranch, name, "not a branch under komodo/")
	return false
}

// branch deletes a local branch that still points at tip and is in the history of in; a branch already gone counts as done.
func (r *run) branch(name, tip, in string) {
	if !r.allowed(name) || !r.proceed() {
		return
	}

	cur, ok, err := r.h.Git.BranchTip(r.ctx, r.h.Clone, name)
	if err != nil {
		r.fail(KindBranch, name, err)
		return
	}
	if !ok {
		return
	}
	if r.dry {
		if cur == tip {
			r.removed(KindBranch, name)
		} else {
			r.kept(KindBranch, name, "branch moved since the tip was read")
		}
		return
	}
	if err := r.h.Git.DeleteBranch(r.ctx, r.h.Clone, name, tip, in); err != nil {
		r.fail(KindBranch, name, err)
		return
	}

	r.removed(KindBranch, name)
}

// redundantBranch deletes a group's branch when the remote holds its tip in any branch; otherwise it keeps the branch.
func (r *run) redundantBranch(name string) {
	if !r.allowed(name) {
		return
	}

	tip, ok, err := r.h.Git.BranchTip(r.ctx, r.h.Clone, name)
	if err != nil {
		r.fail(KindBranch, name, err)
		return
	}
	if !ok {
		return
	}
	if r.noRemote != nil {
		r.kept(KindBranch, name, "remote not reachable: "+r.noRemote.Error())
		return
	}

	ref, held, err := r.h.Git.Holds(r.ctx, r.h.Clone, r.h.Remote, "", tip)
	if err != nil {
		r.fail(KindBranch, name, err)
		return
	}
	if !held {
		r.kept(KindBranch, name, "remote does not hold the branch tip")
		return
	}
	r.branch(name, tip, ref)
}

// epicBranch returns the local name of the epic branch that the remote reference contains names.
func epicBranch(p Published) (string, bool) {
	name := strings.TrimPrefix(p.Contains, p.Remote+"/")
	return name, name != p.Contains && name != ""
}
