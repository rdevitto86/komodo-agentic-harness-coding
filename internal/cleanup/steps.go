package cleanup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// StartedFact is the data of the cleanup.start fact.
type StartedFact struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip"`
}

// DiscardedFact is the data of the group.discarded fact.
type DiscardedFact struct {
	Reason string `json:"reason"`
}

// run is one call's working state: the group it cleans, the report it fills and whether it only lists.
type run struct {
	h     *Handler
	ctx   context.Context
	group string
	rep   *Report
	dry   bool
}

// refused reports whether err is a refusal on purpose rather than a failure.
func refused(err error) bool {
	var r interface{ Refused() bool }
	return errors.As(err, &r) && r.Refused()
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
		h.Errors.Report(fault.Source{Component: "cleanup", Group: bus.ID(group)}, string(format.Internal), fault.KindInternal, err)
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
	if r.h.Boxes == nil {
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

// checkouts removes the group's tester checkout and session temporary folder.
func (r *run) checkouts() {
	for _, c := range []struct{ kind, root string }{{KindTester, r.h.Roots.Testers}, {KindTemp, r.h.Roots.Temp}} {
		if c.root == "" {
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

// worktree removes a worktree under root; force also drops work the tip lacks. A missing worktree counts as removed already.
func (r *run) worktree(root, path string, force bool) {
	if err := inside(root, path); err != nil {
		r.fail(KindWorktree, path, err)
		return
	}
	was := exists(path)
	if r.dry {
		if was {
			r.removed(KindWorktree, path)
		}
		return
	}
	var err error
	if force {
		err = r.h.Git.DiscardWorktree(r.ctx, r.h.Clone, path)
	} else {
		err = r.h.Git.RemoveWorktree(r.ctx, r.h.Clone, path)
	}
	if err != nil {
		r.fail(KindWorktree, path, err)
		return
	}
	if was {
		r.removed(KindWorktree, path)
	}
}

// branch deletes a local branch that still points at tip and is in the history of in; a branch already gone counts as done.
func (r *run) branch(name, tip, in string) {
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
	tip, ok, err := r.h.Git.BranchTip(r.ctx, r.h.Clone, name)
	if err != nil {
		r.fail(KindBranch, name, err)
		return
	}
	if !ok {
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
