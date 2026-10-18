package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// addCheckouts creates the tester checkout and temp folder of a group.
func (e *env) addCheckouts(g grp) (tester, temp string) {
	tester = filepath.Join(e.roots.Testers, g.id)
	temp = filepath.Join(e.roots.Temp, g.id)
	e.write(filepath.Join(tester, "copy.txt"), "x")
	e.write(filepath.Join(temp, "scratch.txt"), "x")
	return tester, temp
}

func TestCompleteNotPublishedRemovesNothing(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if !errors.Is(err, ErrNotPublished) {
		t.Fatalf("err = %v, want ErrNotPublished", err)
	}
	if len(rep.Removed) != 0 || !exists(g.wt) || !e.hasBranch(testBranch) {
		t.Fatalf("something was removed: %+v", rep)
	}
	if got := e.log.names("container.", "git.Remove", "git.Delete", "git.Discard", "task."); len(got) != 0 {
		t.Fatalf("calls = %v, want none", got)
	}
}

func TestCompleteRemovesInOrderAndClosesLast(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	tester, temp := e.addCheckouts(g)
	e.land(g)
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"container.Remove g1", "git.RemoveWorktree " + g.wt, "git.DeleteBranch " + testBranch, "task.Close"}
	if got := e.log.names(
		"container.",
		"git.RemoveWorktree",
		"git.DeleteBranch",
		"git.Discard",
		"task.",
	); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for _, p := range []string{g.wt, tester, temp} {
		if exists(p) {
			t.Fatalf("%s still exists", p)
		}
	}
	if e.hasBranch(testBranch) {
		t.Fatal("branch survived")
	}
	var kinds []string
	for _, it := range rep.Removed {
		kinds = append(kinds, it.Kind)
	}
	if wantK := []string{KindContainer, KindTester, KindTemp, KindWorktree, KindBranch}; !reflect.DeepEqual(kinds, wantK) {
		t.Fatalf("removed kinds = %v, want %v", kinds, wantK)
	}
	if len(rep.Kept) != 0 {
		t.Fatalf("kept = %+v", rep.Kept)
	}
	if got := e.rep.subtypes(); !reflect.DeepEqual(got, []string{"cleanup.start", "cleanup.done"}) {
		t.Fatalf("facts = %v", got)
	}
}

func TestCompleteWithoutDocker(t *testing.T) {
	e := newEnv(t)
	e.h.Boxes = nil
	g := e.addGroup("g1", testBranch)
	e.land(g)
	if _, err := e.h.Complete(context.Background(), e.published(g)); err != nil {
		t.Fatal(err)
	}
	if exists(g.wt) || e.hasBranch(testBranch) {
		t.Fatal("worktree or branch survived")
	}
}

func TestCompleteContainerFailureKeepsContainerAndContinues(t *testing.T) {
	e := newEnv(t)
	e.boxes.err = errors.New("docker daemon down")
	g := e.addGroup("g1", testBranch)
	e.land(g)
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Kind != KindContainer || rep.Kept[0].Reason != "docker daemon down" {
		t.Fatalf("kept = %+v", rep.Kept)
	}
	if exists(g.wt) || e.errs.count() != 1 {
		t.Fatalf("worktree exists %v, errors %d", exists(g.wt), e.errs.count())
	}
}

func TestCompleteTouchesOnlyReadSideAndCloseMethod(t *testing.T) {
	if n := reflect.TypeOf((*Leases)(nil)).Elem().NumMethod(); n != 1 {
		t.Fatalf("Leases has %d methods, want only Holder", n)
	}
	if n := reflect.TypeOf((*TaskGroup)(nil)).Elem().NumMethod(); n != 1 {
		t.Fatalf("TaskGroup has %d methods, want only CloseTaskGroup", n)
	}
}

func TestCompleteRepeatAfterFailureFinishesTheRest(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.git.failOn["git.DeleteBranch"] = errors.New("disk hiccup")
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Kind != KindBranch || !e.hasBranch(testBranch) || exists(g.wt) {
		t.Fatalf("first pass: kept %+v, branch %v", rep.Kept, e.hasBranch(testBranch))
	}
	if e.errs.count() != 1 {
		t.Fatalf("Error Handler got %d errors, want 1", e.errs.count())
	}
	rep, err = e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if e.hasBranch(testBranch) || len(rep.Kept) != 0 {
		t.Fatalf("repeat left branch %v, kept %+v", e.hasBranch(testBranch), rep.Kept)
	}
}

func TestCompleteEpicDoneRemovesLandingAndEpicBranch(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.must(e.clone, "fetch", "origin")
	landing := filepath.Join(e.roots.Landings, "ep-1")
	e.must(e.clone, "worktree", "add", "-b", testEpic, landing, epicRef)
	p := e.published(g)
	p.EpicDone, p.Landing, p.PRURL = true, landing, "https://github.com/acme/shop/pull/41"
	before := e.remoteRefs()
	rep, err := e.h.Complete(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if exists(landing) || e.hasBranch(testEpic) {
		t.Fatalf("landing exists %v, epic branch %v; report %+v", exists(landing), e.hasBranch(testEpic), rep)
	}
	if after := e.remoteRefs(); after != before {
		t.Fatalf("remote changed:\n%s\n--\n%s", before, after)
	}
	if got := e.log.names("task."); len(got) != 1 {
		t.Fatalf("task calls = %v", got)
	}
}

func TestCompleteKeepsWorktreeWithUntrackedFile(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.write(filepath.Join(g.wt, "notes.txt"), "mine")
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(g.wt, "notes.txt")) {
		t.Fatal("untracked file was deleted")
	}
	if len(rep.Kept) != 2 || rep.Kept[0].Kind != KindWorktree || rep.Kept[0].Name != g.wt {
		t.Fatalf("kept = %+v", rep.Kept)
	}
	if e.errs.count() != 0 {
		t.Fatalf("a refusal went to the Error Handler: %d", e.errs.count())
	}
	if got := e.log.names("task."); len(got) != 1 {
		t.Fatal("group was not closed")
	}
}

func TestCompleteKeepsBranchMovedAfterPublish(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.write(filepath.Join(g.wt, "later.txt"), "more")
	e.must(g.wt, "add", "-A")
	e.must(g.wt, "commit", "-m", "later")
	rep, err := e.h.Complete(context.Background(), e.published(g))
	if err != nil {
		t.Fatal(err)
	}
	if !e.hasBranch(testBranch) {
		t.Fatal("moved branch was deleted")
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Kind != KindBranch {
		t.Fatalf("kept = %+v", rep.Kept)
	}
}

func TestCompleteDeletesNoRemoteBranch(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	before := e.remoteRefs()
	if _, err := e.h.Complete(context.Background(), e.published(g)); err != nil {
		t.Fatal(err)
	}
	if after := e.remoteRefs(); after != before {
		t.Fatalf("remote changed:\n%s\n--\n%s", before, after)
	}
}

func TestCompleteCloseFailureReturnsErrorAndSkipsDone(t *testing.T) {
	e := newEnv(t)
	e.task.err = errors.New("sandbox stuck")
	g := e.addGroup("g1", testBranch)
	e.land(g)
	if _, err := e.h.Complete(context.Background(), e.published(g)); err == nil {
		t.Fatal("want an error")
	}
	if e.errs.count() != 1 {
		t.Fatalf("Error Handler got %d errors, want 1", e.errs.count())
	}
	for _, s := range e.rep.subtypes() {
		if s == "cleanup.done" {
			t.Fatal("cleanup.done reported after a failed close")
		}
	}
}

func TestCompleteRefusesPathOutsideRoots(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	outside := filepath.Join(e.base, "precious")
	e.write(filepath.Join(outside, "keep.txt"), "x")
	p := e.published(g)
	p.Worktree = outside
	if _, err := e.h.Complete(context.Background(), p); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("err = %v, want ErrOutsideRoots", err)
	}
	if !exists(filepath.Join(outside, "keep.txt")) || !exists(g.wt) {
		t.Fatal("a path outside the roots was touched")
	}
	if got := e.log.names("container.", "git.", "task."); len(got) != 0 {
		t.Fatalf("calls = %v", got)
	}
}

func TestCompleteRefusesGroupIDThatEscapes(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	victim := filepath.Join(e.base, "home", "victim")
	e.write(filepath.Join(victim, "keep.txt"), "x")
	p := e.published(g)
	p.Group = "../../victim"
	if _, err := e.h.Complete(context.Background(), p); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("err = %v, want ErrOutsideRoots", err)
	}
	if !exists(filepath.Join(victim, "keep.txt")) {
		t.Fatal("folder outside the roots was removed")
	}
}

func TestCompleteSymlinkedCheckoutIsUnlinkedNotFollowed(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	outside := filepath.Join(e.base, "precious")
	e.write(filepath.Join(outside, "keep.txt"), "x")
	if err := os.Symlink(outside, filepath.Join(e.roots.Testers, g.id)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Complete(context.Background(), e.published(g)); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(outside, "keep.txt")) {
		t.Fatal("symlink target was deleted")
	}
	if exists(filepath.Join(e.roots.Testers, g.id)) {
		t.Fatal("symlink survived")
	}
}
