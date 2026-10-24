package cleanup

import (
	"context"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

func TestNoGroupBranchDeletion(t *testing.T) {
	t.Parallel()

	t.Run("Complete deletes no branch", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		g := e.addGroup("g1", testBranch)
		e.land(g)
		rep, err := e.h.Complete(context.Background(), e.published(g))
		if err != nil {
			t.Fatal(err)
		}
		if exists(g.wt) || !e.hasBranch(testBranch) {
			t.Fatalf("worktree exists %v, branch exists %v; report %+v", exists(g.wt), e.hasBranch(testBranch), rep)
		}
		for _, it := range rep.Removed {
			if it.Kind == KindBranch {
				t.Fatalf("removed a branch: %+v", rep.Removed)
			}
		}
	})

	t.Run("Sweep deletes no group branch", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		g := e.addGroup("g1", testBranch)
		e.land(g)
		e.holdLease(g, lease.StateRevoked)
		if _, err := e.h.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if exists(g.wt) || !e.hasBranch(testBranch) {
			t.Fatalf("worktree exists %v, branch exists %v", exists(g.wt), e.hasBranch(testBranch))
		}
	})

	t.Run("Discard deletes no branch", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		g := e.addGroup("g1", testBranch)
		e.holdLease(g, lease.StateRevoked)
		if _, err := e.h.Discard(context.Background(), "g1", DiscardOptions{}); err != nil {
			t.Fatal(err)
		}
		if exists(g.wt) || !e.hasBranch(testBranch) {
			t.Fatalf("worktree exists %v, branch exists %v", exists(g.wt), e.hasBranch(testBranch))
		}
	})
}

func TestSweepEpicBranch(t *testing.T) {
	t.Parallel()

	t.Run("removes komodo/epic only when no unfinished group names it", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		l1, l2 := e.twoLandings()
		e.groups.refs = []GroupRef{{Group: "g9", Version: "1.0.0", Epic: "ep-2"}}
		if _, err := e.h.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if exists(l1) || e.hasBranch("komodo/ep-1") {
			t.Fatal("an epic no unfinished group names keeps its landing or branch")
		}
		if !exists(l2) || !e.hasBranch("komodo/ep-2") {
			t.Fatal("an epic an unfinished group names lost its landing or branch")
		}
	})

	t.Run("keeps every landing when an unfinished group has no epic", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		l1, l2 := e.twoLandings()
		e.groups.refs = []GroupRef{{Group: "g9", Version: "1.0.0"}}
		if _, err := e.h.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !exists(l1) || !exists(l2) || !e.hasBranch("komodo/ep-1") {
			t.Fatal("a landing was removed although a group's epic is unknown")
		}
	})
}
