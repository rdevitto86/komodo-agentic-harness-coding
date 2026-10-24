package cleanup

import (
	"context"
	"path/filepath"
	"testing"
)

// landLocally squashes the group's tree into the local epic branch as one commit and returns that commit.
func (e *env) landLocally(g grp) string {
	e.t.Helper()
	id := e.must(e.clone, "commit-tree", g.tip+"^{tree}", "-p", "main", "-m", "tg: goal")
	e.must(e.clone, "update-ref", "refs/heads/"+testEpic, id)
	return id
}

// A group squashed into the local epic branch is cleaned up with no remote involved: its worktree goes, no branch does.
func TestCompleteLandedLocallyNeedsNoRemote(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	landed := e.landLocally(g)
	p := e.published(g)
	p.Landed, p.Into = landed, testEpic

	rep, err := e.h.Complete(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if exists(g.wt) || !e.hasBranch(testBranch) {
		t.Fatalf("worktree exists %v, branch exists %v; report %+v", exists(g.wt), e.hasBranch(testBranch), rep)
	}
	if got := e.log.names("git.Fetch", "git.Holds", "git.Delete"); len(got) != 0 {
		t.Fatalf("the remote was consulted or a branch deleted: %v", got)
	}
	if got := e.log.names("task."); len(got) != 1 || !e.hasBranch(testEpic) {
		t.Fatalf("task calls = %v, epic branch kept = %v", got, e.hasBranch(testEpic))
	}
}

// CompleteEpic removes the landing worktree and the local epic branch once the remote holds the branch tip.
func TestCompleteEpicRemovesLandingAndBranchWhenPushed(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.land(g)
	e.must(e.clone, "fetch", "origin")
	landing := filepath.Join(e.roots.Landings, "ep-1")
	e.must(e.clone, "worktree", "add", "-b", testEpic, landing, epicRef)

	rep, err := e.h.CompleteEpic(context.Background(), PublishedEpic{
		Epic: "ep-1", Remote: "origin", Contains: epicRef, Landing: landing, PRURL: "https://github.com/acme/shop/pull/41",
	})
	if err != nil {
		t.Fatal(err)
	}
	if exists(landing) || e.hasBranch(testEpic) {
		t.Fatalf("landing exists %v, epic branch %v; report %+v", exists(landing), e.hasBranch(testEpic), rep)
	}
	if got := e.log.names("task."); len(got) != 0 {
		t.Fatalf("an epic cleanup closed a group: %v", got)
	}
}

// CompleteEpic keeps the local epic branch when the remote does not hold its tip, so unpushed work is never deleted.
func TestCompleteEpicKeepsAnUnpushedBranch(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("g1", testBranch)
	e.landLocally(g)

	rep, err := e.h.CompleteEpic(context.Background(), PublishedEpic{Epic: "ep-1", Remote: "origin", Contains: epicRef})
	if err != nil {
		t.Fatal(err)
	}
	if !e.hasBranch(testEpic) || len(rep.Kept) == 0 {
		t.Fatalf("epic branch kept = %v; report %+v", e.hasBranch(testEpic), rep)
	}
}
