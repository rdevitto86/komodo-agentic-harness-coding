package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// sawCall reports whether the log holds a call that starts with prefix.
func (e *env) sawCall(prefix string) bool { return len(e.log.names(prefix)) > 0 }

func TestSweepKeepsUnfinishedGroupWhole(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.holdLease(g, lease.StateRevoked)
	tester, temp := e.addCheckouts(g)
	e.boxes.groups = []string{"g1"}
	e.groups.refs = []GroupRef{{Group: "g1", Version: "1.0.0"}}
	rep, err := e.h.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{g.wt, tester, temp} {
		if !exists(p) {
			t.Fatalf("%s of an unfinished group was removed", p)
		}
	}
	if e.sawCall("container.Remove") || !e.hasBranch(testBranch) {
		t.Fatalf("container or branch of an unfinished group was touched: %v", e.log.all())
	}
	if len(rep.Kept) == 0 || len(rep.Removed) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSweepFailsClosedWithoutGroupReader(t *testing.T) {
	e := newEnv(t)
	e.h.Groups = nil
	g := e.addGroup("g1", testBranch)
	if _, err := e.h.Sweep(context.Background()); err == nil || !exists(g.wt) {
		t.Fatalf("err = %v, worktree exists %v", err, exists(g.wt))
	}
}

// linkedWorktree makes a real dirty worktree outside the roots and links <Worktrees>/g1 to it.
func (e *env) linkedWorktree() string {
	e.t.Helper()
	outside := filepath.Join(e.base, "outside-wt")
	e.must(e.clone, "worktree", "add", "-b", "komodo/ep-1-out", outside, "main")
	e.write(filepath.Join(outside, "dirty.txt"), "mine")
	if err := os.Symlink(outside, filepath.Join(e.roots.Worktrees, "g1")); err != nil {
		e.t.Fatal(err)
	}
	return outside
}

func TestSweepUnlinksLinkedWorktreeWithoutGit(t *testing.T) {
	e := newEnv(t)
	outside := e.linkedWorktree()
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(outside, "dirty.txt")) {
		t.Fatal("the linked worktree was deleted")
	}
	if exists(filepath.Join(e.roots.Worktrees, "g1")) {
		t.Fatal("the link survived")
	}
	if e.sawCall("git.RemoveWorktree") || e.sawCall("git.DiscardWorktree") {
		t.Fatalf("a link was handed to git: %v", e.log.all())
	}
}

func TestDiscardUnlinksLinkedWorktreeWithoutGit(t *testing.T) {
	e := newEnv(t)
	outside := e.linkedWorktree()
	if _, err := e.h.Discard(context.Background(), "g1", DiscardOptions{}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(outside, "dirty.txt")) || e.sawCall("git.DiscardWorktree") {
		t.Fatalf("the linked worktree was reached: %v", e.log.all())
	}
}

func TestDiscardStopsWhenAnotherRunTakesTheGroup(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.holdLease(g, lease.StateRevoked)
	e.h.Live = func(l lease.Lease) bool { return l.Run == "new" }
	e.leases.flipAfter = 1
	_, err := e.h.Discard(context.Background(), "g1", DiscardOptions{})
	if !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("err = %v, want ErrLeaseHeld", err)
	}
	if !exists(g.wt) || !e.hasBranch(testBranch) || e.sawCall("container.Remove") || e.sawCall("task.") {
		t.Fatalf("a group another run took was touched: %v", e.log.all())
	}
}

func TestSweepStopsWhenAnotherRunTakesTheGroup(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.holdLease(g, lease.StateRevoked)
	e.h.Live = func(l lease.Lease) bool { return l.Run == "new" }
	e.leases.flipAfter = 1
	rep, err := e.h.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(g.wt) || e.sawCall("container.Remove") || len(rep.Kept) == 0 {
		t.Fatalf("a group another run took was touched: %v, %+v", e.log.all(), rep)
	}
}

func TestSweepAndDiscardHoldTheGroupLock(t *testing.T) {
	e := newEnv(t)
	e.addGroup("g1", testBranch)
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.addGroup("g2", "komodo/ep-1-g2")
	if _, err := e.h.Discard(context.Background(), "g2", DiscardOptions{}); err != nil {
		t.Fatal(err)
	}
	got := e.log.names("lease.")
	want := []string{"lease.Lock g1", "lease.Unlock g1", "lease.Lock g2", "lease.Unlock g2"}
	if len(got) != len(want) {
		t.Fatalf("lock calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lock calls = %v, want %v", got, want)
		}
	}
}

func TestCompleteRefusesAnotherGroupsWorktree(t *testing.T) {
	e := newEnv(t)
	g1 := e.addGroup("g1", testBranch)
	g2 := e.addGroup("g2", "komodo/ep-1-g2")
	e.holdLease(g2, lease.StateHeld)
	e.land(g1)
	p := e.published(g1)
	p.Worktree = g2.wt
	if _, err := e.h.Complete(context.Background(), p); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("err = %v, want ErrOutsideRoots", err)
	}
	if !exists(g2.wt) || e.sawCall("git.") || e.sawCall("task.") {
		t.Fatalf("another group's worktree was reached: %v", e.log.all())
	}
}

func TestCompleteRefusesLandingThatIsNotTheEpics(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	other := e.addGroup("g2", "komodo/ep-1-g2")
	p := e.published(g)
	p.EpicDone, p.Landing = true, filepath.Join(e.roots.Landings, "ep-9")
	e.write(filepath.Join(p.Landing, "keep.txt"), "x")
	if _, err := e.h.Complete(context.Background(), p); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("err = %v, want ErrOutsideRoots", err)
	}
	if !exists(filepath.Join(p.Landing, "keep.txt")) || !exists(other.wt) {
		t.Fatal("a folder that is not this epic's landing was reached")
	}
}

func TestCompleteKeepsBranchOfKeptWorktree(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.write(filepath.Join(g.wt, "notes.txt"), "mine")
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatal(err)
	}
	if !e.hasBranch(testBranch) {
		t.Fatalf("the branch of a kept worktree was deleted: %+v", rep)
	}
}

func TestCompleteKeepsEpicBranchWhenLandingKept(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.must(e.clone, "fetch", "origin")
	landing := filepath.Join(e.roots.Landings, "ep-1")
	e.must(e.clone, "worktree", "add", "-b", testEpic, landing, epicRef)
	e.write(filepath.Join(landing, "wip.txt"), "x")
	p := e.published(g)
	p.EpicDone, p.Landing = true, landing
	if _, err := e.h.Complete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(landing, "wip.txt")) || !e.hasBranch(testEpic) {
		t.Fatal("a refused landing lost its branch or files")
	}
}

func TestSweepKeepsBranchOfKeptWorktree(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.holdLease(g, lease.StateRevoked)
	e.write(filepath.Join(g.wt, "wip.txt"), "x")
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.hasBranch(testBranch) {
		t.Fatal("the branch of a kept worktree was deleted")
	}
}

func TestUnusedVersionsFailsClosedOnEmptyVersion(t *testing.T) {
	e := newEnv(t)
	e.write(filepath.Join(e.roots.Versions, "1.0.0-beta.4", "komodo"), "bin")
	e.h.Current = "1.0.0"
	e.groups.refs = []GroupRef{{Group: "g1", Version: ""}}
	if got, err := e.h.UnusedVersions(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want an error and nothing", got, err)
	}
}

func TestUnusedVersionsFailsClosedWithoutCurrentVersion(t *testing.T) {
	e := newEnv(t)
	e.write(filepath.Join(e.roots.Versions, "1.0.0-beta.4", "komodo"), "bin")
	if got, err := e.h.UnusedVersions(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want an error and nothing", got, err)
	}
}

func TestUnusedVersionsKeepsCurrentVersion(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"1.0.0-beta.4", "1.0.0"} {
		e.write(filepath.Join(e.roots.Versions, v, "komodo"), "bin")
	}
	e.h.Current = "1.0.0"
	got, err := e.h.UnusedVersions(context.Background())
	if err != nil || len(got) != 1 || got[0] != "1.0.0-beta.4" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestCompleteNeverDeletesBranchOutsideKomodo(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.must(e.clone, "push", "origin", g.tip+":refs/heads/main")
	e.must(e.clone, "fetch", "origin")
	p := e.published(g)
	p.Contains, p.EpicDone = "origin/main", true
	if _, err := e.h.Complete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !e.hasBranch("main") {
		t.Fatal("local main was deleted")
	}
	p.Branch = "main"
	if _, err := e.h.Complete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !e.hasBranch("main") {
		t.Fatal("a group branch named main was deleted")
	}
}

func TestSweepClearsLeaksOfGroupsWithoutWorktree(t *testing.T) {
	e := newEnv(t)
	e.boxes.groups = []string{"c1"}
	e.write(filepath.Join(e.roots.Testers, "t1", "x"), "x")
	e.write(filepath.Join(e.roots.Temp, "p1", "x"), "x")
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.sawCall("container.Remove c1") {
		t.Fatalf("a leaked container was not removed: %v", e.log.all())
	}
	if exists(filepath.Join(e.roots.Testers, "t1")) || exists(filepath.Join(e.roots.Temp, "p1")) {
		t.Fatal("leaked checkouts survive")
	}
}

// twoLandings creates the landing worktrees of epics ep-1 and ep-2 with their local branches.
func (e *env) twoLandings() (l1, l2 string) {
	e.t.Helper()
	e.must(e.clone, "push", "origin", "main:refs/heads/komodo/ep-2")
	e.must(e.clone, "fetch", "origin")
	l1, l2 = filepath.Join(e.roots.Landings, "ep-1"), filepath.Join(e.roots.Landings, "ep-2")
	e.must(e.clone, "worktree", "add", "-b", "komodo/ep-1", l1, "origin/komodo/ep-1")
	e.must(e.clone, "worktree", "add", "-b", "komodo/ep-2", l2, "origin/komodo/ep-2")
	return l1, l2
}

func TestSweepClearsLandingNoUnfinishedGroupNeeds(t *testing.T) {
	e := newEnv(t)
	l1, l2 := e.twoLandings()
	live := e.addGroup("g9", "komodo/ep-2-tg-1.1")
	e.holdLease(live, lease.StateRevoked)
	e.groups.refs = []GroupRef{{Group: "g9", Version: "1.0.0"}}
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exists(l1) || e.hasBranch("komodo/ep-1") {
		t.Fatal("a dead epic's landing or branch survives")
	}
	if !exists(l2) || !e.hasBranch("komodo/ep-2") {
		t.Fatal("the landing of an epic with an unfinished group was removed")
	}
}

func TestSweepKeepsEveryLandingWhenAnUnfinishedGroupHasNoLease(t *testing.T) {
	e := newEnv(t)
	l1, l2 := e.twoLandings()
	e.groups.refs = []GroupRef{{Group: "ghost", Version: "1.0.0"}}
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !exists(l1) || !exists(l2) {
		t.Fatal("a landing was removed although an unfinished group may still need it")
	}
}

func TestSweepFetchesOnceAndPreviewNeverPrunes(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"g1", "g2", "g3"} {
		g := e.addGroup(id, "komodo/ep-1-"+id)
		e.holdLease(g, lease.StateRevoked)
	}
	e.must(e.clone, "update-ref", "refs/remotes/origin/stale", e.must(e.clone, "rev-parse", "main"))
	if _, err := e.h.Preview(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(e.clone, "rev-parse", "--verify", "-q", "refs/remotes/origin/stale"); err != nil {
		t.Fatal("Preview pruned a remote-tracking reference")
	}
	e.log.calls = nil
	if _, err := e.h.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.log.names("git.Fetch"); len(got) != 1 {
		t.Fatalf("fetches = %v, want exactly 1", got)
	}
}
