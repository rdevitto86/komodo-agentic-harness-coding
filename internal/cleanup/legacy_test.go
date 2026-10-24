package cleanup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// legacyGroup creates a group's worktree in the old short-name folder.
func legacyGroup(e *env, id string) grp {
	e.t.Helper()
	fresh := e.roots.Worktrees
	e.roots.Worktrees = e.h.Roots.Legacy
	defer func() { e.roots.Worktrees = fresh }()
	return e.addGroup(id, "komodo/ep-1-"+id)
}

// newLegacyEnv wires the old short-name worktree folder next to the full-name one.
func newLegacyEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.h.Roots.Legacy = filepath.Join(e.base, "home", "wt", "old-shop")
	return e
}

func TestSweepMigratesWorktreesOfTheOldFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a worktree no live lease holds is removed and listed", func(t *testing.T) {
		t.Parallel()
		e := newLegacyEnv(t)
		g := legacyGroup(e, "g1")
		e.land(g)
		rep, err := e.h.Sweep(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if exists(g.wt) {
			t.Fatalf("the old worktree survives: %+v", rep)
		}
		if !containsItem(rep.Removed, Item{Kind: KindWorktree, Name: g.wt}) {
			t.Fatalf("removal not listed: %+v", rep.Removed)
		}
	})

	t.Run("a worktree a live lease holds is never touched", func(t *testing.T) {
		t.Parallel()
		e := newLegacyEnv(t)
		g := legacyGroup(e, "g1")
		e.holdLease(g, lease.StateHeld)
		e.h.Live = liveHolders("g1")
		rep, err := e.h.Clean(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !exists(g.wt) || containsItem(rep.Removed, Item{Kind: KindWorktree, Name: g.wt}) {
			t.Fatalf("a live worktree was removed: %+v", rep)
		}
		for _, c := range e.log.all() {
			if c == "git.RemoveWorktree "+g.wt {
				t.Fatalf("live group call: %s", c)
			}
		}
	})

	t.Run("a group a run can continue is kept", func(t *testing.T) {
		t.Parallel()
		e := newLegacyEnv(t)
		g := legacyGroup(e, "g1")
		e.groups.refs = []GroupRef{{Group: "g1"}}
		if _, err := e.h.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if !exists(g.wt) {
			t.Fatal("an unfinished group's worktree was removed")
		}
	})

	t.Run("a dry run lists the old worktree and keeps it", func(t *testing.T) {
		t.Parallel()
		e := newLegacyEnv(t)
		g := legacyGroup(e, "g1")
		rep, err := e.h.Preview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !exists(g.wt) || !containsItem(rep.Removed, Item{Kind: KindWorktree, Name: g.wt}) {
			t.Fatalf("preview removed the worktree or missed it: %+v", rep)
		}
	})

	t.Run("an unreadable old folder is listed", func(t *testing.T) {
		t.Parallel()
		e := newLegacyEnv(t)
		e.write(e.h.Roots.Legacy, "not a folder")
		rep, err := e.h.Sweep(ctx)
		if err == nil && len(rep.Kept) == 0 {
			t.Fatalf("a bad old folder passed silently: %+v", rep)
		}
	})
}

// containsItem reports whether items holds want.
func containsItem(items []Item, want Item) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}
