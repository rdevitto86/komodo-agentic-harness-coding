package commit

import (
	"errors"
	"testing"
)

// saveReq builds a save request for the group.
func saveReq(g group, task string) SaveRequest {
	return SaveRequest{
		Run:      "run-1",
		Group:    bindID(g.id),
		Repo:     "repo",
		Label:    g.label,
		Task:     task,
		Worktree: g.wt,
		Branch:   g.branch,
		Fence:    g.fence,
	}
}

// Save commits the worktree on the group branch, pushes nothing and leaves the remote unchanged.
func TestSaveCommitsLocallyAndPushesNothing(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	h := w.handler()
	remoteBefore := git(t, w.remote, "for-each-ref")
	w.work(g, "a.txt", "a")
	res, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Head == "" || res.Tree == "" {
		t.Fatalf("result = %+v", res)
	}
	if got := git(t, w.clone, "rev-parse", g.branch); got != res.Head {
		t.Fatalf("branch tip = %s, want %s", got, res.Head)
	}
	if subj := git(t, w.clone, "log", "-1", "--format=%s", g.branch); subj != "tg-1.1: tsk-1.1.1" {
		t.Fatalf("subject = %q", subj)
	}
	if after := git(t, w.remote, "for-each-ref"); after != remoteBefore {
		t.Fatalf("remote changed:\n%s\nvs\n%s", remoteBefore, after)
	}
	if len(w.gh.calls()) != 0 {
		t.Fatalf("forge called: %v", w.gh.calls())
	}
	if len(w.clean.calls) != 0 {
		t.Fatal("cleanup reached")
	}
}

// A second Save with an unchanged worktree commits nothing and returns Saved false.
func TestSaveUnchangedWorktreeCommitsNothing(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	h := w.handler()
	w.work(g, "a.txt", "a")
	first, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Saved || second.Head != first.Head || second.Tree != first.Tree {
		t.Fatalf("second = %+v, first = %+v", second, first)
	}
	w.work(g, "b.txt", "b")
	third, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.2"))
	if err != nil || !third.Saved || third.Head == first.Head {
		t.Fatalf("third = %+v, %v", third, err)
	}
}

// Save with a stale fence, another holder or no lease returns ErrLeaseLost and commits nothing.
func TestSaveRefusesLostLease(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	h := w.handler()
	w.work(g, "a.txt", "a")
	before := git(t, w.clone, "rev-parse", g.branch)
	w.leases.bump(g.id, 4)
	_, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.1"))
	var pe *PublishError
	if !errors.Is(err, ErrLeaseLost) || !errors.As(err, &pe) || pe.Kind != KindLeaseLost || pe.Retryable {
		t.Fatalf("stale fence err = %v", err)
	}
	w.leases.bump(g.id, g.fence)
	w.leases.set(g, "run-2", w.clone)
	if _, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("other holder err = %v", err)
	}
	delete(w.leases.m, g.id)
	if _, err := h.Save(w.ctx, saveReq(g, "tsk-1.1.1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("no lease err = %v", err)
	}
	if after := git(t, w.clone, "rev-parse", g.branch); after != before {
		t.Fatalf("branch moved %s -> %s", before, after)
	}
}

// Save refuses a branch outside komodo/ before git runs.
func TestSaveRefusesCriticalBranch(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	h := w.handler()
	w.work(g, "a.txt", "a")
	r := saveReq(g, "tsk-1.1.1")
	r.Branch = "main"
	n := len(w.traced)
	_, err := h.Save(w.ctx, r)
	var pe *PublishError
	if !errors.Is(err, ErrCriticalRef) || !errors.As(err, &pe) || pe.Kind != KindCriticalRef {
		t.Fatalf("err = %v", err)
	}
	if len(w.traced) != n {
		t.Fatalf("git ran: %v", w.traced[n:])
	}
}
