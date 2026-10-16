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

// maxSyncWalk bounds how many merge-from-epic commits Publish walks back to find the approved commit.
const maxSyncWalk = 16

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

// Publish commits the approved tree, lands it on the epic branch, pushes, opens or updates the epic's pull request after the last landing, then hands the group to the Cleanup Handler.
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

// validate refuses an approval whose verdicts disagree or whose names would leave the komodo/ branches.
func (p *publication) validate() error {
	a := p.a
	if !validBranch(a.Branch) || !validBranch(p.epic) || strings.ContainsAny(a.Epic, "/\\") {
		return fail(KindCriticalRef, false, fmt.Errorf("branch %q or epic %q: %w", a.Branch, a.Epic, ErrCriticalRef))
	}
	if a.Tree == "" || a.Tree != a.Tester.Tree || a.Tree != a.Reviewer.Tree {
		return fail(KindNotApproved, false, fmt.Errorf("verdicts name different trees: %w", ErrNotApproved))
	}
	if a.Label == "" || !slices.Contains(a.EpicGroups, a.Label) {
		return fail(KindNotApproved, false, fmt.Errorf("group %q is not among the epic's groups: %w", a.Label, ErrNotApproved))
	}
	return nil
}

// settle resolves the base branch and the remote, refuses a group or epic branch that is the base branch, and picks the landing worktree path.
func (p *publication) settle(ctx context.Context) error {
	cfg := p.h.Config
	p.base, p.remote = cfg.BaseBranch, cfg.Remote
	root := cfg.LandingRoot
	if root == "" {
		root = filepath.Dir(p.a.Worktree)
	}
	p.wt = filepath.Join(root, "landing-"+p.a.Epic)
	if err := resolveRemote(ctx, p.h.Git, p.a.Worktree, &p.base, &p.remote); err != nil {
		return err
	}
	if p.base == p.a.Branch || p.base == p.epic {
		return fail(KindCriticalRef, false, fmt.Errorf("base branch %q is a group or epic branch: %w", p.base, ErrCriticalRef))
	}
	return nil
}

// resolveRemote fills an empty base branch from the remote's default branch and an empty remote from the base branch's upstream.
func resolveRemote(ctx context.Context, g Git, dir string, base, remote *string) error {
	if *base == "" {
		r := *remote
		if r == "" {
			r = "origin"
		}
		b, err := g.DefaultBranch(ctx, dir, r)
		if err != nil {
			return fail(KindBaseMissing, false, fmt.Errorf("find the remote's default branch: %w", err))
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

// tracking names a remote-tracking reference in full, so no other reference of the same short name can win.
func (p *publication) tracking(branch string) string {
	return "refs/remotes/" + p.remote + "/" + branch
}

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
	notApproved := fail(KindNotApproved, false, fmt.Errorf("worktree tree %s, approved %s: %w", wtTree, a.Tree, ErrNotApproved))
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
		return fail(KindNotApproved, false, fmt.Errorf("worktree is on %s, approved %s: %w", cur, p.a.Branch, ErrNotApproved))
	}
	return nil
}

// holdsApproved reports whether the approved commit lies behind HEAD through only this group's merges of the epic branch.
// Each such merge must be a real merge commit whose second parent is a commit the remote epic branch already holds.
func (p *publication) holdsApproved(ctx context.Context, cur CommitInfo) bool {
	syncSubject := p.a.Label + ": merge " + p.a.Epic
	for i := 0; i < maxSyncWalk; i++ {
		if cur.Tree == p.a.Tree {
			return true
		}
		if cur.Subject != syncSubject || cur.Parent == "" || cur.Second == "" {
			return false
		}
		held, err := p.h.Git.IsAncestor(ctx, p.a.Worktree, cur.Second, p.tracking(p.epic))
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

// fetchBranch fetches one remote branch and maps a missing one to base_missing and a network failure to a retryable push error.
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

// recheck runs the free checks on the landing worktree; a failure pauses the epic under this group.
// The check result decides the outcome, whether or not the pause could be reported.
func (p *publication) recheck(ctx context.Context, turn *Turn) error {
	res, err := p.h.Check.Recheck(ctx, p.a.Epic, p.wt)
	if err != nil {
		turn.Release()
		return fail(KindRecheck, true, fmt.Errorf("run the checks: %w", err))
	}
	if res.Pass {
		return nil
	}
	turn.Finish(false)
	failed := fmt.Errorf("%w: %s", ErrRecheck, res.Output)
	if rerr := p.report(ctx, "epic.paused", bus.EpicPaused{Epic: p.a.Epic, Group: p.a.Label, Output: res.Output}); rerr != nil {
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

// land runs the landing turn, the push, the recheck, the pull request, the report and the hand-off.
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
	if err := p.fetchBranch(ctx, a.Worktree, p.epic); err != nil {
		return Result{}, err
	}
	if err := h.Git.AddWorktree(ctx, p.clone, p.wt, p.epic, p.remote+"/"+p.epic); err != nil {
		return Result{}, gitFail(err)
	}
	if _, err := h.Git.Merge(ctx, p.wt, p.tracking(p.epic), a.Epic+": update from "+p.remote, false); err != nil {
		return Result{}, gitFail(err)
	}
	tip, _, err := h.Git.Head(ctx, a.Worktree)
	if err != nil {
		return Result{}, gitFail(err)
	}
	var ls []Landing
	already, err := h.Git.IsAncestor(ctx, p.wt, tip, "HEAD")
	if err != nil {
		return Result{}, gitFail(err)
	}
	if already {
		if ls, err = h.Git.Landed(ctx, p.wt, p.tracking(p.base), "HEAD"); err != nil {
			return Result{}, gitFail(err)
		}
		if already = p.hasLanded(ls); !already {
			ls = nil
		}
	}
	if turn.First() && !already {
		if err := p.recheck(ctx, turn); err != nil {
			freed = true
			return Result{}, err
		}
	}
	if !already {
		if err := p.merge(ctx); err != nil {
			return Result{}, err
		}
	}
	head, err := p.push(ctx)
	if err != nil {
		return Result{}, err
	}
	if ls == nil {
		if ls, err = h.Git.Landed(ctx, p.wt, p.tracking(p.base), "HEAD"); err != nil {
			return Result{}, gitFail(err)
		}
	}
	last := p.isLast(ls)
	var epicStat string
	if last {
		if epicStat, err = h.Git.DiffStat(ctx, p.wt, p.tracking(p.base), "HEAD"); err != nil {
			return Result{}, gitFail(err)
		}
	}
	wasPaused := turn.Paused()
	if err := p.recheck(ctx, turn); err != nil {
		freed = true
		return Result{}, err
	}
	turn.Finish(true)
	freed = true
	if wasPaused {
		if err := p.report(ctx, "epic.resumed", bus.EpicResumed{Epic: a.Epic, Group: a.Label}); err != nil {
			return Result{}, err
		}
	}
	res := Result{Branch: p.epic, Head: head, Last: last}
	if last {
		if err := p.pullRequest(ctx, ls, epicStat, &res); err != nil {
			return Result{}, err
		}
	}
	return p.finish(ctx, res)
}

// merge brings the epic branch into the group branch, then merges the verified group commit into the epic branch in the landing worktree.
func (p *publication) merge(ctx context.Context) error {
	g, a := p.h.Git, p.a
	if err := p.onGroupBranch(ctx); err != nil {
		return err
	}
	if _, err := g.Merge(ctx, a.Worktree, p.tracking(p.epic), a.Label+": merge "+a.Epic, false); err != nil {
		return gitFail(err)
	}
	groupTip, _, err := g.Head(ctx, a.Worktree)
	if err != nil {
		return gitFail(err)
	}
	section, err := RenderBody(a.Facts, a.Tester, a.Reviewer)
	if err != nil {
		return fail(KindGit, false, err)
	}
	if _, err := g.Merge(ctx, p.wt, groupTip, a.Label+": "+oneLine(a.Goal)+"\n\n"+section, true); err != nil {
		return gitFail(err)
	}
	return nil
}

// push re-reads the lease, then pushes the epic branch unless the remote already holds the local tip, and returns that tip.
func (p *publication) push(ctx context.Context) (string, error) {
	g, a := p.h.Git, p.a
	if _, err := p.h.owns(ctx, a.Repo, a.Group, a.Run, a.Fence); err != nil {
		return "", err
	}
	local, _, err := g.Head(ctx, p.wt)
	if err != nil {
		return "", gitFail(err)
	}
	tip, ok, err := g.RemoteTip(ctx, p.wt, p.remote, p.epic)
	if err != nil {
		return "", fail(KindPush, true, err)
	}
	if !ok || tip != local {
		if err := g.Push(ctx, p.wt, p.remote, p.epic); err != nil {
			if errors.Is(err, ErrPushRejected) {
				return "", gitFail(err)
			}
			return "", fail(KindPush, true, err)
		}
		if tip, ok, err = g.RemoteTip(ctx, p.wt, p.remote, p.epic); err != nil || !ok || tip != local {
			return "", fail(KindPush, true, fmt.Errorf("remote does not hold %s after the push", local))
		}
	}
	if err := p.fetchBranch(ctx, p.wt, p.epic); err != nil {
		return "", err
	}
	return local, nil
}

// hasLanded reports whether the list holds this group's landing.
func (p *publication) hasLanded(ls []Landing) bool {
	return slices.ContainsFunc(ls, func(l Landing) bool { return l.Label == p.a.Label })
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

// pullRequest opens the epic's pull request, or refreshes its body when one is open.
func (p *publication) pullRequest(ctx context.Context, ls []Landing, stat string, res *Result) error {
	f := EpicFacts{DiffStat: stat}
	for i := len(ls) - 1; i >= 0; i-- {
		if slices.Contains(p.a.EpicGroups, ls[i].Label) {
			f.Groups = append(f.Groups, ls[i].Section)
		}
	}
	body, err := RenderEpicBody(f)
	if err != nil {
		return fail(KindForge, false, err)
	}
	title := fmt.Sprintf("%s: %d task groups", p.a.Epic, len(p.a.EpicGroups))
	label := p.h.Config.PRLabel
	if label == "" {
		label = "komodo"
	}
	pr, found, err := p.h.Forge.FindPR(ctx, p.epic)
	if err != nil {
		return fail(KindForge, true, err)
	}
	if found {
		if err := p.h.Forge.UpdatePR(ctx, pr.URL, title, body); err != nil {
			return fail(KindForge, true, err)
		}
	} else {
		if pr, err = p.h.Forge.OpenPR(ctx, p.base, p.epic, title, body, label); err != nil {
			return fail(KindForge, true, err)
		}
		res.PRNew = true
	}
	res.PRURL = pr.URL
	return nil
}

// finish reports the result and hands the group to the Cleanup Handler; a cleanup error comes back unchanged.
// pr.opened goes out on every publish that finds the epic complete and its pull request open, so a lost report or a crash after the pull request cannot strand the run.
func (p *publication) finish(ctx context.Context, res Result) (Result, error) {
	a := p.a
	if err := p.report(ctx, "publish.done", res); err != nil {
		return Result{}, err
	}
	if res.Last && res.PRURL != "" {
		if err := p.report(ctx, "pr.opened", res); err != nil {
			return Result{}, err
		}
	}
	if p.h.Cleanup == nil {
		return res, nil
	}
	tip, _, err := p.h.Git.Head(ctx, a.Worktree)
	if err != nil {
		return Result{}, gitFail(err)
	}
	err = p.h.Cleanup.Complete(ctx, Published{
		Run: a.Run, Group: a.Group, Epic: a.Epic, Branch: a.Branch, Tip: tip, Remote: p.remote,
		Contains: p.remote + "/" + p.epic, Worktree: a.Worktree, EpicDone: res.Last, PRURL: res.PRURL,
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}
