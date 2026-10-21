package core

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// landWorld is a landing worktree on the epic branch and a group with one commit not yet landed.
type landWorld struct {
	*world
	land string
	g    group
}

// newLandWorld builds a landing worktree and a group that committed a.txt.
func newLandWorld(t *testing.T) landWorld {
	t.Helper()
	w := newWorld(t)
	land := filepath.Join(w.root, "land")
	if err := w.git.AddWorktree(w.ctx, w.clone, land, "komodo/ep-1", "origin/komodo/ep-1"); err != nil {
		t.Fatal(err)
	}
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: a"); err != nil {
		t.Fatal(err)
	}
	return landWorld{world: w, land: land, g: g}
}

// tip returns the group branch tip.
func (l landWorld) tip() string { return git(l.t, l.g.wt, "rev-parse", "HEAD") }

// hasBranch reports whether the group branch exists in the clone.
func (l landWorld) hasBranch() bool {
	_, ok, _ := l.git.BranchTip(l.ctx, l.clone, l.g.branch)
	return ok
}

// epicCommit adds one unrelated commit to the epic branch.
func (l landWorld) epicCommit(msg string) {
	write(l.t, l.land, strings.ReplaceAll(msg, " ", "-")+".txt", msg)
	git(l.t, l.land, "add", "-A")
	git(l.t, l.land, "commit", "-m", msg)
}

// squash lands the group on the epic branch as one commit.
func (l landWorld) squash() {
	if _, err := l.git.Squash(l.ctx, l.land, l.g.branch, "tg-1.1: Goal"); err != nil {
		l.t.Fatal(err)
	}
}

// DeleteLanded deletes a group branch only when the epic branch holds a commit with the group's whole change.
func TestDeleteLandedByContent(t *testing.T) {
	deleted := func(t *testing.T, l landWorld, tip string) {
		t.Helper()
		if err := l.git.DeleteLanded(l.ctx, l.clone, l.g.branch, tip, "komodo/ep-1"); err != nil || l.hasBranch() {
			t.Fatalf("err = %v, branch kept = %v", err, l.hasBranch())
		}
	}
	kept := func(t *testing.T, l landWorld, tip string) {
		t.Helper()
		err := l.git.DeleteLanded(l.ctx, l.clone, l.g.branch, tip, "komodo/ep-1")
		if !errors.Is(err, ErrNotContained) || !l.hasBranch() {
			t.Fatalf("err = %v, branch kept = %v", err, l.hasBranch())
		}
	}

	t.Run("the squashed commit is present", func(t *testing.T) {
		l := newLandWorld(t)
		l.squash()
		deleted(t, l, l.tip())
	})
	t.Run("the squashed commit is absent", func(t *testing.T) {
		l := newLandWorld(t)
		l.epicCommit("another change")
		kept(t, l, l.tip())
	})
	t.Run("later commits follow the squash", func(t *testing.T) {
		l := newLandWorld(t)
		l.squash()
		l.epicCommit("next group")
		deleted(t, l, l.tip())
	})
	t.Run("the group merged an earlier landing before its own", func(t *testing.T) {
		l := newLandWorld(t)
		l.epicCommit("earlier group")
		git(t, l.g.wt, "merge", "--no-edit", "komodo/ep-1")
		l.squash()
		deleted(t, l, l.tip())
	})
	t.Run("the group committed again after landing", func(t *testing.T) {
		l := newLandWorld(t)
		l.squash()
		l.work(l.g, "b.txt", "b")
		if _, err := l.git.Commit(l.ctx, l.g.wt, "tg-1.1: more"); err != nil {
			t.Fatal(err)
		}
		kept(t, l, l.tip())
	})
	t.Run("the epic branch holds the branch by ancestry", func(t *testing.T) {
		l := newLandWorld(t)
		tip := l.tip()
		if _, err := l.git.Merge(l.ctx, l.land, l.g.branch, "ep-1: merge", false); err != nil {
			t.Fatal(err)
		}
		deleted(t, l, tip)
	})
	t.Run("the group added only an empty commit after its work landed", func(t *testing.T) {
		l := newLandWorld(t)
		if _, err := l.git.Merge(l.ctx, l.land, l.g.branch, "ep-1: merge", false); err != nil {
			t.Fatal(err)
		}
		git(t, l.g.wt, "commit", "--allow-empty", "-m", "tg-1.1: nothing")
		deleted(t, l, l.tip())
	})
	t.Run("a moved tip is refused", func(t *testing.T) {
		l := newLandWorld(t)
		l.squash()
		err := l.git.DeleteLanded(l.ctx, l.clone, l.g.branch, strings.Repeat("a", 40), "komodo/ep-1")
		if !errors.Is(err, ErrTipMoved) || !l.hasBranch() {
			t.Fatalf("err = %v, branch kept = %v", err, l.hasBranch())
		}
	})
}
