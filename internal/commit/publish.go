package commit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// publication is the state of one Publish call.
type publication struct {
	h      *Handler
	a      Approval
	clone  string
	remote string
	base   string
	epic   string // epic branch, komodo/<epic>
	wt     string // landing worktree path
}

// Publish commits the approved tree, lands the group as one commit on the local epic branch, then hands the group to the
// Cleanup Handler. It never pushes and opens no pull request; PushEpic does both once the epic's groups have landed.
func (h *Handler) Publish(ctx context.Context, a Approval) (Result, error) {
	p := &publication{h: h, a: a, epic: "komodo/" + a.Epic}
	if err := p.validate(); err != nil {
		return Result{}, err
	}

	l, err := h.owns(ctx, a.Repo, a.Group, a.Run, a.Fence)
	if err != nil {
		return Result{}, err
	}

	p.clone = l.ClonePath
	h.adopt(p.clone)

	if err := p.settle(ctx); err != nil {
		return Result{}, err
	}
	if err := p.commit(ctx); err != nil {
		return Result{}, err
	}
	return p.land(ctx)
}

// validate refuses an approval whose verdicts disagree or whose names would leave the komodo/
// branches.
func (p *publication) validate() error {
	a := p.a
	if !validBranch(a.Branch) || !validBranch(p.epic) || strings.ContainsAny(a.Epic, "/\\") {
		return fail(
			KindCriticalRef,
			false,
			fmt.Errorf("branch %q or epic %q: %w", a.Branch, a.Epic, ErrCriticalRef),
		)
	}

	if a.Tree == "" || a.Tree != a.QA.Tree || a.Tree != a.Reviewer.Tree {
		return fail(
			KindNotApproved,
			false,
			fmt.Errorf("verdicts name different trees: %w", ErrNotApproved),
		)
	}

	if a.Label == "" || !slices.Contains(a.EpicGroups, a.Label) {
		return fail(
			KindNotApproved,
			false,
			fmt.Errorf("group %q is not among the epic's groups: %w", a.Label, ErrNotApproved),
		)
	}

	return nil
}

// settle resolves the base branch and the remote, refuses a group or epic branch that is the base
// branch, and picks the landing worktree path.
func (p *publication) settle(ctx context.Context) error {
	cfg := p.h.settings()
	p.base, p.remote = cfg.BaseBranch, cfg.Remote

	root := cfg.LandingRoot
	if root == "" {
		root = filepath.Dir(p.a.Worktree)
	}

	p.wt = filepath.Join(root, p.a.Epic)
	if err := resolveRemote(ctx, p.h.Git, p.a.Worktree, &p.base, &p.remote); err != nil {
		return err
	}

	if p.base == p.a.Branch || p.base == p.epic {
		return fail(
			KindCriticalRef,
			false,
			fmt.Errorf("base branch %q is a group or epic branch: %w", p.base, ErrCriticalRef),
		)
	}

	return nil
}

// tracking names a remote-tracking reference in full, so no other reference of the same short name
// can win.
func (p *publication) tracking(branch string) string {
	return "refs/remotes/" + p.remote + "/" + branch
}

// local names a local branch in full, so a tag of the same short name cannot win.
func (p *publication) local(branch string) string { return "refs/heads/" + branch }

// commit records the approved tree on the group branch unless a commit already holds it.
func (p *publication) commit(ctx context.Context) error {
	g, a := p.h.Git, p.a
	if err := p.onGroupBranch(ctx); err != nil {
		return err
	}

	wtTree, err := g.TreeOf(ctx, a.Worktree)
	if err != nil {
		return gitFail(err)
	}

	head, err := g.Info(ctx, a.Worktree, "HEAD")
	if err != nil {
		return gitFail(err)
	}

	notApproved := fail(
		KindNotApproved,
		false,
		fmt.Errorf("worktree tree %s, approved %s: %w", wtTree, a.Tree, ErrNotApproved),
	)

	switch {
	case wtTree == a.Tree && head.Tree == a.Tree:
		return nil
	case wtTree == a.Tree:
		if _, err := g.Commit(ctx, a.Worktree, a.Label+": "+oneLine(a.Goal)); err != nil {
			return gitFail(err)
		}
		if _, tree, err := g.Head(ctx, a.Worktree); err != nil || tree != a.Tree {
			return notApproved
		}
		return nil
	case wtTree == head.Tree && p.holdsApproved(ctx, head):
		return nil
	}

	return notApproved
}

// onGroupBranch refuses a worktree that does not have the approved group branch checked out.
func (p *publication) onGroupBranch(ctx context.Context) error {
	cur, err := p.h.Git.CurrentBranch(ctx, p.a.Worktree)
	if err != nil {
		return gitFail(err)
	}

	if cur != p.a.Branch {
		return fail(
			KindNotApproved,
			false,
			fmt.Errorf("worktree is on %s, approved %s: %w", cur, p.a.Branch, ErrNotApproved),
		)
	}
	return nil
}

// holdsApproved reports whether the approved commit lies behind HEAD through only this group's
// merges of the epic branch. Each such merge must be a real merge commit whose second parent is a
// commit the local epic branch already holds.
func (p *publication) holdsApproved(ctx context.Context, cur CommitInfo) bool {
	// maxSyncWalk bounds how many merge-from-epic commits Publish walks back to find the approved
	// commit.
	const maxSyncWalk = 16

	syncSubject := p.a.Label + ": merge " + p.a.Epic

	for i := 0; i < maxSyncWalk; i++ {
		if cur.Tree == p.a.Tree {
			return true
		}
		if cur.Subject != syncSubject || cur.Parent == "" || cur.Second == "" {
			return false
		}

		held, err := p.h.Git.IsAncestor(ctx, p.a.Worktree, cur.Second, p.local(p.epic))
		if err != nil || !held {
			return false
		}

		next, err := p.h.Git.Info(ctx, p.a.Worktree, cur.Parent)
		if err != nil {
			return false
		}
		cur = next
	}
	return false
}

// fetchBranch fetches one remote branch and maps a missing one to base_missing and a network
// failure to a retryable push error.
func (p *publication) fetchBranch(ctx context.Context, dir, branch string) error {
	err := p.h.Git.FetchBranch(ctx, dir, p.remote, branch)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBranchMissing):
		return fail(KindBaseMissing, false, err)
	}
	return fail(KindPush, true, err)
}

// recheck runs the free checks on the landing worktree; a failure pauses the epic under this group. landed says the group's
// landing commit is the epic tip. The check result decides the outcome, whether or not the pause could be reported.
func (p *publication) recheck(ctx context.Context, turn *Turn, landed bool) error {
	res, err := p.h.Check.Recheck(ctx, p.a.Epic, p.wt, landed)
	if err != nil {
		turn.Release()
		return fail(KindRecheck, true, fmt.Errorf("run the checks: %w", err))
	}

	if res.Pass {
		return nil
	}

	turn.Finish(false)

	failed := fmt.Errorf("%w: %s", ErrRecheck, res.Output)
	if rerr := p.report(
		ctx,
		"epic.paused",
		bus.EpicPaused{Epic: p.a.Epic, Group: p.a.Label, Output: res.Output},
	); rerr != nil {
		failed = errors.Join(failed, rerr)
	}

	return fail(KindRecheck, false, failed)
}

// report sends one progress fact through the Reporter.
func (p *publication) report(ctx context.Context, subtype string, data any) error {
	if p.h.Report == nil {
		return nil
	}
	if err := p.h.Report.Report(ctx, p.a.Group, subtype, data); err != nil {
		return fail(KindReport, true, fmt.Errorf("report %s: %w", subtype, err))
	}
	return nil
}

// land runs the landing turn, the local landing, the recheck, the report and the hand-off.
func (p *publication) land(ctx context.Context) (Result, error) {
	h, a := p.h, p.a

	turn, err := h.Epics.Enter(ctx, a.Repo+"/"+a.Epic, a.Label)
	if err != nil {
		return Result{}, fail(KindGit, true, fmt.Errorf("wait for the epic's turn: %w", err))
	}

	freed := false
	defer func() {
		if !freed {
			turn.Release()
		}
	}()

	if err := p.fetchBranch(ctx, a.Worktree, p.base); err != nil {
		return Result{}, err
	}
	if err := p.syncEpic(ctx); err != nil {
		return Result{}, err
	}

	ls, err := h.Git.Landed(ctx, p.wt, p.tracking(p.base), "HEAD")
	if err != nil {
		return Result{}, gitFail(err)
	}

	already, err := p.holdsLanding(ctx, ls)
	if err != nil {
		return Result{}, err
	}
	if turn.First() && !already {
		if err := p.recheck(ctx, turn, false); err != nil {
			freed = true
			return Result{}, err
		}
	}

	if !already {
		id, err := p.merge(ctx)
		if err != nil {
			return Result{}, err
		}

		ls = append([]Landing{{ID: id, Label: a.Label, Goal: oneLine(a.Goal)}}, ls...)
	}

	head, _, err := h.Git.Head(ctx, p.wt)
	if err != nil {
		return Result{}, gitFail(err)
	}

	// A landing that later groups have built on was rechecked when it landed; the tip is no longer its tree.
	wasPaused := turn.Paused()
	if !already || head == p.landedID(ls) {
		if err := p.recheck(ctx, turn, true); err != nil {
			freed = true
			return Result{}, err
		}
	}

	turn.Finish(true)
	freed = true

	if wasPaused {
		if err := p.report(
			ctx, "epic.resumed", bus.EpicResumed{Epic: a.Epic, Group: a.Label},
		); err != nil {
			return Result{}, err
		}
	}

	res := Result{Branch: p.epic, Head: head, Landed: p.landedID(ls), Last: p.isLast(ls)}

	return p.finish(ctx, res)
}

// syncEpic fetches the remote epic branch and merges what the remote holds into the landing worktree's local epic branch;
// a remote that has no epic branch yet leaves the local branch as it is.
func (p *publication) syncEpic(ctx context.Context) error {
	h := p.h
	if err := h.Git.FetchBranch(ctx, p.a.Worktree, p.remote, p.epic); err != nil {
		if !errors.Is(err, ErrBranchMissing) {
			return fail(KindPush, true, err)
		}
		return p.addLanding(ctx, p.local(p.epic))
	}

	if err := p.addLanding(ctx, p.tracking(p.epic)); err != nil {
		return err
	}

	if _, err := h.Git.Merge(
		ctx, p.wt, p.tracking(p.epic), p.a.Epic+": update from "+p.remote, false,
	); err != nil {
		return gitFail(err)
	}

	return nil
}

// addLanding makes sure the landing worktree has the local epic branch checked out, cutting it from base when the branch
// does not exist yet.
func (p *publication) addLanding(ctx context.Context, base string) error {
	if err := p.h.Git.AddWorktree(ctx, p.clone, p.wt, p.epic, base); err != nil {
		return gitFail(err)
	}

	return nil
}

// merge brings the local epic branch into the group branch, then squashes the verified group tip into the epic branch in the
// landing worktree as one commit, and returns that commit's id. The lease is read again just before, so a stale holder never
// lands.
func (p *publication) merge(ctx context.Context) (string, error) {
	g, a := p.h.Git, p.a
	if err := p.onGroupBranch(ctx); err != nil {
		return "", err
	}

	if _, err := g.Merge(
		ctx, a.Worktree, p.local(p.epic), a.Label+": merge "+a.Epic, false,
	); err != nil {
		return "", gitFail(err)
	}

	groupTip, _, err := g.Head(ctx, a.Worktree)
	if err != nil {
		return "", gitFail(err)
	}

	section, err := RenderBody(a.Facts, a.QA, a.Reviewer)
	if err != nil {
		return "", fail(KindGit, false, err)
	}

	if _, err := p.h.owns(ctx, a.Repo, a.Group, a.Run, a.Fence); err != nil {
		return "", err
	}

	id, err := g.Squash(ctx, p.wt, groupTip, a.Label+": "+oneLine(a.Goal)+"\n\n"+section)
	if err != nil {
		return "", gitFail(err)
	}

	// Recording the landing in the group branch lets a later landing of the same group, after a fix, merge the epic cleanly.
	if _, err := g.Merge(ctx, a.Worktree, id, a.Label+": merge "+a.Epic, false); err != nil {
		return "", gitFail(err)
	}

	return id, nil
}

// holdsLanding reports whether the group's newest landing on the epic holds the tree of the group's checkout, so a group
// approved again with a fix lands again.
func (p *publication) holdsLanding(ctx context.Context, ls []Landing) (bool, error) {
	id := p.landedID(ls)
	if id == "" {
		return false, nil
	}

	landed, err := p.h.Git.Info(ctx, p.wt, id)
	if err != nil {
		return false, gitFail(err)
	}

	_, tree, err := p.h.Git.Head(ctx, p.a.Worktree)
	if err != nil {
		return false, gitFail(err)
	}

	return landed.Tree == tree, nil
}

// landedID returns the id of this group's landing commit, or empty when the list has none.
func (p *publication) landedID(ls []Landing) string {
	for _, l := range ls {
		if l.Label == p.a.Label {
			return l.ID
		}
	}

	return ""
}

// isLast reports whether every group label of the epic has landed.
func (p *publication) isLast(ls []Landing) bool {
	got := map[string]bool{}
	for _, l := range ls {
		got[l.Label] = true
	}

	for _, label := range p.a.EpicGroups {
		if !got[label] {
			return false
		}
	}

	return true
}

// finish reports the landing and hands the group to the Cleanup Handler; a cleanup error comes back unchanged, and a repeat
// of the same approval reaches the Cleanup Handler again.
func (p *publication) finish(ctx context.Context, res Result) (Result, error) {
	a := p.a
	if err := p.report(ctx, "publish.done", res); err != nil {
		return Result{}, err
	}

	if p.h.Cleanup == nil {
		return res, nil
	}

	tip, _, err := p.h.Git.Head(ctx, a.Worktree)
	if err != nil {
		return Result{}, gitFail(err)
	}

	err = p.h.Cleanup.Complete(ctx, Published{
		Run:      a.Run,
		Group:    a.Group,
		Epic:     a.Epic,
		Branch:   a.Branch,
		Tip:      tip,
		Landed:   res.Landed,
		Into:     p.epic,
		Worktree: a.Worktree,
	})
	if err != nil {
		return Result{}, err
	}

	return res, nil
}

// resolveRemote fills an empty base branch from the remote's default branch and an empty remote
// from the base branch's upstream.
func resolveRemote(ctx context.Context, g Git, dir string, base, remote *string) error {
	if *base == "" {
		r := *remote
		if r == "" {
			r = "origin"
		}

		b, err := g.DefaultBranch(ctx, dir, r)
		if err != nil {
			return fail(
				KindBaseMissing,
				false,
				fmt.Errorf("find the remote's default branch: %w", err),
			)
		}

		*base = b
	}

	if *remote == "" {
		r, ok, err := g.UpstreamRemote(ctx, dir, *base)
		if err != nil {
			return gitFail(err)
		}
		if !ok {
			r = "origin"
		}
		*remote = r
	}

	return nil
}
