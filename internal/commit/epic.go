package commit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// epicPush is the state of one PushEpic call.
type epicPush struct {
	h      *Handler
	r      EpicPush
	remote string
	base   string
	label  string // pull request label as settled; empty means komodo
	epic   string // epic branch, komodo/<epic>
	wt     string // worktree with the epic branch checked out
}

// PushEpic pushes the epic branch once with every group landed on it, then opens the epic's pull request or refreshes it
// when every group has landed. It is the only place the Commit Handler pushes or calls the forge, so CI runs once per pull
// request. Local landings stay in the epic branch until a call here reaches the remote, so a crash loses nothing.
func (h *Handler) PushEpic(ctx context.Context, r EpicPush) (EpicResult, error) {
	e := &epicPush{h: h, r: r, epic: "komodo/" + r.Epic}
	if !validBranch(e.epic) || strings.ContainsAny(r.Epic, "/\\") {
		return EpicResult{}, fail(KindCriticalRef, false, fmt.Errorf("epic branch %q: %w", e.epic, ErrCriticalRef))
	}

	h.adopt(r.CommonDir)
	h.adopt(r.Root)

	cfg := h.settings()
	e.base, e.remote, e.label = cfg.BaseBranch, cfg.Remote, cfg.PRLabel

	if err := resolveRemote(ctx, h.Git, r.Root, &e.base, &e.remote); err != nil {
		return EpicResult{}, err
	}
	if e.base == e.epic {
		return EpicResult{}, fail(
			KindCriticalRef,
			false,
			fmt.Errorf("base branch %q is the epic branch: %w", e.base, ErrCriticalRef),
		)
	}

	res, err := e.run(ctx)
	if err != nil {
		return EpicResult{}, err
	}

	if res.Last && res.PRURL != "" && h.Cleanup != nil {
		err := h.Cleanup.CompleteEpic(ctx, PublishedEpic{
			Epic:     r.Epic,
			Remote:   e.remote,
			Contains: e.remote + "/" + e.epic,
			PRURL:    res.PRURL,
		})
		if err != nil {
			res.CleanupError = err.Error()
		}
	}

	return res, nil
}

// run holds the epic's turn while it brings the remote's commits in, pushes and settles the pull request.
func (e *epicPush) run(ctx context.Context) (EpicResult, error) {
	h, r := e.h, e.r

	turn, err := h.Epics.Lock(ctx, r.Repo+"/"+r.Epic)
	if err != nil {
		return EpicResult{}, fail(KindGit, true, fmt.Errorf("wait for the epic's turn: %w", err))
	}
	defer turn.Release()

	wt, release, err := h.epicWorktree(ctx, r.CommonDir, e.epic, e.remote)
	if err != nil {
		return EpicResult{}, err
	}
	defer release()

	e.wt = wt

	if err := e.syncRemote(ctx); err != nil {
		return EpicResult{}, err
	}
	if err := e.fetchBase(ctx); err != nil {
		return EpicResult{}, err
	}

	ls, err := h.Git.Landed(ctx, e.wt, "refs/remotes/"+e.remote+"/"+e.base, "HEAD")
	if err != nil {
		return EpicResult{}, gitFail(err)
	}

	res := EpicResult{Branch: e.epic, Landed: e.landedLabels(ls)}
	res.Last = len(r.EpicGroups) > 0 && len(res.Landed) == len(r.EpicGroups)

	head, _, err := h.Git.Head(ctx, e.wt)
	if err != nil {
		return EpicResult{}, gitFail(err)
	}

	res.Head = head
	if len(res.Landed) == 0 {
		return res, nil
	}

	if res.Pushed, err = e.push(ctx, head); err != nil {
		return EpicResult{}, err
	}
	if res.Last && e.allDone() && !h.Epics.Paused(r.Repo+"/"+r.Epic) {
		if err := e.pullRequest(ctx, ls, &res); err != nil {
			return EpicResult{}, err
		}
	}

	return res, nil
}

// syncRemote fetches the remote epic branch and merges what it holds into the local branch, so the push is a fast-forward;
// a remote with no epic branch yet changes nothing.
func (e *epicPush) syncRemote(ctx context.Context) error {
	err := e.h.Git.FetchBranch(ctx, e.wt, e.remote, e.epic)
	switch {
	case err == nil:
	case errors.Is(err, ErrBranchMissing):
		return nil
	default:
		return fail(KindPush, true, err)
	}

	if _, err := e.h.Git.Merge(
		ctx, e.wt, "refs/remotes/"+e.remote+"/"+e.epic, e.r.Epic+": update from "+e.remote, false,
	); err != nil {
		return gitFail(err)
	}

	return nil
}

// fetchBase fetches the base branch, which the landed groups and the diff stat are read against.
func (e *epicPush) fetchBase(ctx context.Context) error {
	err := e.h.Git.FetchBranch(ctx, e.wt, e.remote, e.base)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBranchMissing):
		return fail(KindBaseMissing, false, err)
	}

	return fail(KindPush, true, err)
}

// allDone reports whether the Runtime Manager holds every group of the epic Done, so a group whose last recheck failed and
// is blocked never gets a pull request.
func (e *epicPush) allDone() bool {
	for _, label := range e.r.EpicGroups {
		if !slices.Contains(e.r.Done, label) {
			return false
		}
	}

	return true
}

// landedLabels returns the labels of the epic's groups the list holds, in landing order.
func (e *epicPush) landedLabels(ls []Landing) []string {
	var out []string

	for i := len(ls) - 1; i >= 0; i-- {
		if slices.Contains(e.r.EpicGroups, ls[i].Label) && !slices.Contains(out, ls[i].Label) {
			out = append(out, ls[i].Label)
		}
	}

	return out
}

// push pushes the epic branch unless the remote already holds the local tip, and reports whether it pushed.
func (e *epicPush) push(ctx context.Context, local string) (bool, error) {
	g := e.h.Git

	tip, ok, err := g.RemoteTip(ctx, e.wt, e.remote, e.epic)
	if err != nil {
		return false, fail(KindPush, true, err)
	}
	if ok && tip == local {
		return false, nil
	}

	if err := g.Push(ctx, e.wt, e.remote, e.epic); err != nil {
		if errors.Is(err, ErrPushRejected) {
			return false, gitFail(err)
		}
		return false, fail(KindPush, true, err)
	}

	if tip, ok, err = g.RemoteTip(ctx, e.wt, e.remote, e.epic); err != nil || !ok || tip != local {
		return false, fail(KindPush, true, fmt.Errorf("remote does not hold %s after the push", local))
	}

	return true, nil
}

// pullRequest opens the epic's pull request, or refreshes its body when one is open and the push changed the branch.
func (e *epicPush) pullRequest(ctx context.Context, ls []Landing, res *EpicResult) error {
	stat, err := e.h.Git.DiffStat(ctx, e.wt, "refs/remotes/"+e.remote+"/"+e.base, "HEAD")
	if err != nil {
		return gitFail(err)
	}

	f := EpicFacts{DiffStat: stat}

	// A group landed again after a fix keeps only its newest section.
	var seen []string

	for _, l := range ls {
		if slices.Contains(e.r.EpicGroups, l.Label) && !slices.Contains(seen, l.Label) {
			seen = append(seen, l.Label)
			f.Groups = append([]string{l.Section}, f.Groups...)
		}
	}

	body, err := RenderEpicBody(f)
	if err != nil {
		return fail(KindForge, false, err)
	}

	title := fmt.Sprintf("%s: %d task groups", e.r.Epic, len(e.r.EpicGroups))

	label := e.label
	if label == "" {
		label = "komodo"
	}

	pr, found, err := e.h.Forge.FindPR(ctx, e.epic)
	if err != nil {
		return fail(KindForge, true, err)
	}

	switch {
	case !found:
		if pr, err = e.h.Forge.OpenPR(ctx, e.base, e.epic, title, body, label); err != nil {
			return fail(KindForge, true, err)
		}

		res.PRNew = true
	case res.Pushed:
		if err := e.h.Forge.UpdatePR(ctx, pr.URL, title, body); err != nil {
			return fail(KindForge, true, err)
		}
	}

	res.PRURL = pr.URL

	return nil
}
