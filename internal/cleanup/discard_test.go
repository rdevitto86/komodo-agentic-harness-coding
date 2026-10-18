package cleanup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

func TestDiscardRefusesGroupALiveRunHolds(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.holdLease(g, lease.StateHeld)
	e.h.Live = liveHolders("g1")
	_, err := e.h.Discard(context.Background(), "g1", DiscardOptions{Reason: "abandon"})
	if !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("err = %v, want ErrLeaseHeld", err)
	}
	if !exists(g.wt) || !e.hasBranch(testBranch) {
		t.Fatal("a live group was touched")
	}
	if got := e.log.names("container.", "git.", "task."); len(got) != 0 {
		t.Fatalf("calls = %v", got)
	}
}

func TestDiscardDropsUnpublishedWork(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.holdLease(g, lease.StateRevoked)
	e.write(filepath.Join(g.wt, "wip.txt"), "unsaved")
	state := filepath.Join(e.clone, ".git", "komodo", "runs", "g1", "state.json")
	e.write(state, "{}")
	rep, err := e.h.Discard(context.Background(), "g1", DiscardOptions{Reason: "abandon"})
	if err != nil {
		t.Fatal(err)
	}
	if exists(g.wt) || e.hasBranch(testBranch) {
		t.Fatalf("worktree or branch survived: %+v", rep)
	}
	if !exists(state) {
		t.Fatal("the group's state record was removed")
	}
	if got := e.log.names("container.", "git.Discard", "task."); len(got) != 4 || got[0] != "container.Remove g1" ||
		got[3] != "task.Close" {
		t.Fatalf("calls = %v", got)
	}
	if got := e.rep.subtypes(); len(got) != 1 || got[0] != "group.discarded" {
		t.Fatalf("facts = %v", got)
	}
	if d, ok := e.rep.facts[0].data.(DiscardedFact); !ok || d.Reason != "abandon" {
		t.Fatalf("fact data = %#v", e.rep.facts[0].data)
	}
}

func TestDiscardKeepWorktreeLeavesCheckout(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.holdLease(g, lease.StateRevoked)
	rep, err := e.h.Discard(context.Background(), "g1", DiscardOptions{KeepWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(g.wt, "work-g1.txt")) {
		t.Fatal("kept worktree lost its files")
	}
	var kept bool
	for _, it := range rep.Kept {
		kept = kept || (it.Kind == KindWorktree && it.Name == g.wt)
	}
	if !kept {
		t.Fatalf("kept = %+v", rep.Kept)
	}
	if !e.hasBranch(testBranch) {
		t.Fatal("the branch of a kept worktree was deleted")
	}
}

func TestDiscardRefusesGroupIDThatEscapes(t *testing.T) {
	e := newEnv(t)
	if _, err := e.h.Discard(context.Background(), "../x", DiscardOptions{}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("err = %v, want ErrOutsideRoots", err)
	}
}
