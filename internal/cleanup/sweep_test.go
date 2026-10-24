package cleanup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// liveHolders makes Live count the leases of the named groups as live.
func liveHolders(groups ...string) func(lease.Lease) bool {
	return func(l lease.Lease) bool {
		for _, g := range groups {
			if l.Group == g {
				return true
			}
		}
		return false
	}
}

func TestSweepSkipsGroupAliveRunHolds(t *testing.T) {
	e := newEnv(t)
	live := e.addGroup("live", "komodo/ep-1-live")
	dead := e.addGroup("dead", "komodo/ep-1-dead")
	e.holdLease(live, lease.StateHeld)
	e.holdLease(dead, lease.StateHeld)
	e.h.Live = liveHolders("live")
	e.land(dead)
	rep, err := e.h.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(live.wt) {
		t.Fatal("a live group was touched")
	}
	if exists(dead.wt) || !e.hasBranch("komodo/ep-1-dead") {
		t.Fatalf("the dead group's remains survive: %+v", rep)
	}
	var keptLive bool
	for _, it := range rep.Kept {
		keptLive = keptLive || (it.Name == live.wt && it.Reason != "")
	}
	if !keptLive {
		t.Fatalf("live group not listed under kept: %+v", rep.Kept)
	}
	for _, c := range e.log.all() {
		if c == "container.Remove live" || c == "git.RemoveWorktree "+live.wt {
			t.Fatalf("live group call: %s", c)
		}
	}
	if got := e.log.names("task."); len(got) != 0 {
		t.Fatalf("Sweep called the Task Manager: %v", got)
	}
	if got := e.rep.subtypes(); len(got) != 1 || got[0] != "cleanup.swept" {
		t.Fatalf("facts = %v", got)
	}
}

func TestSweepKeepsDirtyWorktree(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.holdLease(g, lease.StateRevoked)
	e.write(filepath.Join(g.wt, "wip.txt"), "x")
	rep, err := e.h.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(g.wt, "wip.txt")) {
		t.Fatal("dirty worktree was removed")
	}
	if len(rep.Kept) == 0 || rep.Kept[0].Kind != KindWorktree {
		t.Fatalf("kept = %+v", rep.Kept)
	}
}

func TestSweepWithoutLeaseRecordRemovesWorktree(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	rep, err := e.h.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exists(g.wt) || len(rep.Kept) != 0 {
		t.Fatalf("worktree exists %v, kept %+v", exists(g.wt), rep.Kept)
	}
}

func TestSweepIgnoresFilesAndOddNames(t *testing.T) {
	e := newEnv(t)
	e.write(filepath.Join(e.roots.Worktrees, "stray.txt"), "x")
	rep, err := e.h.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(e.roots.Worktrees, "stray.txt")) || len(rep.Removed)+len(rep.Kept) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPreviewListsWithoutRemoving(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.holdLease(g, lease.StateRevoked)
	qa, temp := e.addCheckouts(g)
	rep, err := e.h.Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{g.wt, qa, temp} {
		if !exists(p) {
			t.Fatalf("%s was removed", p)
		}
	}
	if !e.hasBranch(testBranch) {
		t.Fatal("branch was deleted")
	}
	kinds := map[string]bool{}
	for _, it := range rep.Removed {
		kinds[it.Kind] = true
	}
	for _, k := range []string{KindContainer, KindQA, KindTemp, KindWorktree} {
		if !kinds[k] {
			t.Errorf("preview lacks kind %s: %+v", k, rep.Removed)
		}
	}
	if got := e.log.names("container.", "git.RemoveWorktree", "git.DeleteBranch", "task."); len(got) != 0 {
		t.Fatalf("a dry run mutated: %v", got)
	}
	if len(e.rep.subtypes()) != 0 {
		t.Fatalf("a dry run reported: %v", e.rep.subtypes())
	}
}
