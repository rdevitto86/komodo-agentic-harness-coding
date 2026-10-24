package core

import (
	"errors"
	"path/filepath"
	"testing"
)

// Squash lands a group of 2 commits as one commit on the epic branch, with the group's tree and no second parent.
func TestSquashLandsOneCommit(t *testing.T) {
	w := newWorld(t)
	land := filepath.Join(w.root, "land")
	if err := w.git.AddWorktree(w.ctx, w.clone, land, "komodo/ep-1", "origin/komodo/ep-1"); err != nil {
		t.Fatal(err)
	}
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: first"); err != nil {
		t.Fatal(err)
	}
	w.work(g, "b.txt", "b")
	if _, err := w.git.Commit(w.ctx, g.wt, "tg-1.1: second"); err != nil {
		t.Fatal(err)
	}
	_, groupTree, _ := w.git.Head(w.ctx, g.wt)
	before := git(t, land, "rev-parse", "HEAD")

	id, err := w.git.Squash(w.ctx, land, g.branch, "tg-1.1: Goal\n\n"+SectionMarker+"\n2 files")
	if err != nil {
		t.Fatal(err)
	}

	info, err := w.git.Info(w.ctx, land, "HEAD")
	if err != nil || info.ID != id || info.Parent != before || info.Second != "" || info.Tree != groupTree {
		t.Fatalf("head = %+v, %v; want parent %s tree %s", info, err, before, groupTree)
	}
	if n := git(t, land, "rev-list", "--count", before+"..HEAD"); n != "1" {
		t.Fatalf("%s commits landed, want 1", n)
	}
	if got := git(t, land, "log", "-1", "--format=%s"); got != "tg-1.1: Goal" {
		t.Fatalf("subject = %q", got)
	}
	ls, err := w.git.Landed(w.ctx, land, "origin/main", "HEAD")
	if err != nil || len(ls) != 1 || ls[0].Label != "tg-1.1" || ls[0].Section != SectionMarker+"\n2 files" {
		t.Fatalf("landed = %+v, %v", ls, err)
	}
	if _, err := w.git.Merge(w.ctx, land, g.branch, "ep-1: again", false); err != nil {
		t.Fatalf("the epic tip must hold the group's tree: %v", err)
	}
}

// Squash refuses a ref whose history lacks the branch tip, so no work outside the ref is dropped.
func TestSquashRefusesADivergedRef(t *testing.T) {
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
	write(t, land, "e.txt", "e")
	git(t, land, "add", "-A")
	git(t, land, "commit", "-m", "epic moved")
	before := git(t, land, "rev-parse", "HEAD")

	_, err := w.git.Squash(w.ctx, land, g.branch, "tg-1.1: Goal")
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("err = %v, want ErrDiverged", err)
	}
	if git(t, land, "rev-parse", "HEAD") != before {
		t.Fatal("the branch moved")
	}
}
