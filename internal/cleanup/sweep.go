package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// sweep visits every group that has a container, checkout or folder left and cleans the ones that no run can continue.
func (h *Handler) sweep(ctx context.Context, dry bool) (Report, error) {
	rep := Report{}
	if h.Roots.Worktrees == "" {
		return rep, errors.New("no worktree folder to sweep")
	}
	if h.Groups == nil {
		return rep, errors.New("no group reader to name the groups still in use")
	}
	used, err := h.Groups.Unfinished(ctx)
	if err != nil {
		return rep, fmt.Errorf("list unfinished groups: %w", err)
	}
	unfinished := map[string]bool{}
	for _, g := range used {
		unfinished[g.Group] = true
	}
	// One non-pruning fetch serves every branch decision; if it fails, branches are kept.
	fetchErr := h.Git.Fetch(ctx, h.Clone, h.Remote)
	names, err := h.leftovers(ctx, &rep)
	if err != nil {
		return rep, err
	}
	for _, g := range names {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		h.sweepGroup(ctx, &rep, g, unfinished, dry, fetchErr)
	}
	h.sweepLandings(ctx, &rep, used, dry, fetchErr)
	return rep, nil
}

// leftovers returns the sorted group ids found in the worktree, tester and temporary roots and among the containers.
func (h *Handler) leftovers(ctx context.Context, rep *Report) ([]string, error) {
	set := map[string]bool{}
	for _, root := range []string{h.Roots.Worktrees, h.Roots.Testers, h.Roots.Temp} {
		names, err := folders(root)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			set[n] = true
		}
	}
	if h.Boxes != nil {
		groups, err := h.Boxes.List(ctx)
		if err != nil {
			rep.Kept = append(rep.Kept, Item{Kind: KindContainer, Reason: "list containers: " + err.Error()})
		}
		for _, g := range groups {
			if validName(g) {
				set[g] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// folders returns the plain-named folders and links in root; stray files and odd names are ignored, and an empty or missing root has none.
func folders(root string) ([]string, error) {
	if root == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", root, err)
	}
	var out []string
	for _, e := range ents {
		if (e.IsDir() || e.Type()&os.ModeSymlink != 0) && validName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// sweepGroup cleans one group's remains, or lists them when dry; a group a run can continue or a live run holds is kept untouched.
func (h *Handler) sweepGroup(ctx context.Context, rep *Report, group string, unfinished map[string]bool, dry bool, fetchErr error) {
	path := filepath.Join(h.Roots.Worktrees, group)
	if unfinished[group] {
		rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: "a run can still continue the group"})
		return
	}
	if !dry {
		unlock, err := h.lockGroup(ctx, group)
		if err != nil {
			rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: err.Error()})
			return
		}
		defer unlock()
	}
	l, have, live, err := h.holder(ctx, group)
	if err != nil {
		rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: err.Error()})
		return
	}
	if live {
		rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: ErrLeaseHeld.Error()})
		return
	}
	r := &run{h: h, ctx: ctx, group: group, rep: rep, dry: dry, guard: true, noRemote: fetchErr}
	r.container()
	r.checkouts()
	gone := r.worktree(h.Roots.Worktrees, path, false)
	switch {
	case !have || l.Branch == "":
		r.kept(KindBranch, "", "no lease record names the group's branch")
	case gone && r.proceed():
		r.redundantBranch(l.Branch)
	default:
		r.keepBranch(l.Branch)
	}
}

// sweepLandings removes the landing worktree and local branch of every epic that no unfinished group lands on.
// If an unfinished group's branch cannot be read, every landing is kept.
func (h *Handler) sweepLandings(ctx context.Context, rep *Report, used []GroupRef, dry bool, fetchErr error) {
	epics, err := folders(h.Roots.Landings)
	if err != nil || len(epics) == 0 {
		if err != nil {
			rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: h.Roots.Landings, Reason: err.Error()})
		}
		return
	}
	var branches []string
	unknown := false
	for _, g := range used {
		l, have, _, err := h.holder(ctx, g.Group)
		if err != nil || !have || l.Branch == "" {
			unknown = true
			continue
		}
		branches = append(branches, l.Branch)
	}
	for _, epic := range epics {
		path := filepath.Join(h.Roots.Landings, epic)
		if reason := landingInUse(epic, branches, unknown); reason != "" {
			rep.Kept = append(rep.Kept, Item{Kind: KindWorktree, Name: path, Reason: reason})
			continue
		}
		r := &run{h: h, ctx: ctx, group: epic, rep: rep, dry: dry, noRemote: fetchErr}
		if r.worktree(h.Roots.Landings, path, false) {
			r.redundantBranch("komodo/" + epic)
		} else {
			r.keepBranch("komodo/" + epic)
		}
	}
}

// landingInUse says why an epic's landing worktree must stay, or returns empty when no unfinished group lands on the epic.
func landingInUse(epic string, branches []string, unknown bool) string {
	if unknown {
		return "an unfinished group's branch is unknown"
	}
	for _, b := range branches {
		if strings.HasPrefix(b, "komodo/"+epic+"-") {
			return "an unfinished group lands on this epic"
		}
	}
	return ""
}
