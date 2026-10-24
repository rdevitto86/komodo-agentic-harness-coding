package commit

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// countCmd counts the recorded git command lines that hold the token as one argument.
func (w *world) countCmd(token string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, a := range w.traced {
		for _, s := range a {
			if s == token {
				n++
				break
			}
		}
	}
	return n
}

// sideBranch pushes a komodo/ branch from a second clone that adds one file to the epic's base.
func (w *world) sideBranch(name, file string) {
	w.t.Helper()
	other := filepath.Join(w.root, "side-"+strings.ReplaceAll(name, "/", "-"))
	git(w.t, w.root, "clone", w.remote, other)
	git(w.t, other, "checkout", "-b", name, "origin/komodo/ep-1")
	write(w.t, other, file, "x")
	git(w.t, other, "add", "-A")
	git(w.t, other, "commit", "-m", "side")
	git(w.t, other, "push", "origin", name)
}

// A reference of the same name under refs/komodo/ cannot redirect the landing: the verified commit is what merges.
func TestPublishMergesVerifiedCommit(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	w.sideBranch("komodo/decoy", "evil.txt")
	git(t, w.clone, "fetch", "origin", "komodo/decoy")
	git(t, w.clone, "update-ref", "refs/komodo/ep-1-tg-1.1", "FETCH_HEAD")
	if _, err := h.Publish(w.ctx, w.approval(g, "tg-1.1")); err != nil {
		t.Fatal(err)
	}
	tip := w.localTip()
	if out, err := execOut(w.clone, "cat-file", "-e", tip+":evil.txt"); err == nil {
		t.Fatalf("the decoy landed: %s", out)
	}
	if out, err := execOut(w.clone, "cat-file", "-e", tip+":a.txt"); err != nil {
		t.Fatalf("the verified work did not land: %s", out)
	}
}

// A worktree that is not on the approved group branch is refused, for Save and for Publish.
func TestWorktreeOnOtherBranchRefused(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	git(t, g.wt, "checkout", "-q", "-b", "komodo/other")
	before := git(t, w.clone, "rev-parse", "komodo/other")
	if _, err := h.Publish(w.ctx, a); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("publish err = %v", err)
	}
	if _, err := h.Save(w.ctx, saveReq(g, "tsk-1")); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("save err = %v", err)
	}
	if git(t, w.clone, "rev-parse", "komodo/other") != before {
		t.Fatal("a commit landed on the other branch")
	}
}

// Content added after approval cannot ride in under a commit or merge that only looks like the sync of the epic branch.
func TestPublishRejectsSmuggledSyncCommit(t *testing.T) {
	t.Run("plain commit with the sync subject", func(t *testing.T) {
		w := newWorld(t)
		h := w.handler()
		g := w.newGroup("tg-1.1")
		w.work(g, "a.txt", "a")
		if _, err := h.Save(w.ctx, saveReq(g, "tsk-1")); err != nil {
			t.Fatal(err)
		}
		a := w.approval(g, "tg-1.1")
		write(t, g.wt, "evil.txt", "evil")
		git(t, g.wt, "add", "-A")
		git(t, g.wt, "commit", "-q", "-m", "tg-1.1: merge ep-1")
		tip := w.localTip()
		if _, err := h.Publish(w.ctx, a); !errors.Is(err, ErrNotApproved) {
			t.Fatalf("err = %v", err)
		}
		if w.localTip() != tip {
			t.Fatal("the epic moved")
		}
	})
	t.Run("merge of a commit the epic does not hold", func(t *testing.T) {
		w := newWorld(t)
		h := w.handler()
		g := w.newGroup("tg-1.1")
		w.work(g, "a.txt", "a")
		if _, err := h.Save(w.ctx, saveReq(g, "tsk-1")); err != nil {
			t.Fatal(err)
		}
		a := w.approval(g, "tg-1.1")
		w.sideBranch("komodo/side", "evil.txt")
		git(t, g.wt, "fetch", "-q", "origin", "komodo/side")
		git(t, g.wt, "merge", "-q", "--no-ff", "-m", "tg-1.1: merge ep-1", "FETCH_HEAD")
		tip := w.localTip()
		if _, err := h.Publish(w.ctx, a); !errors.Is(err, ErrNotApproved) {
			t.Fatalf("err = %v", err)
		}
		if w.localTip() != tip {
			t.Fatal("the epic moved")
		}
	})
}

// A failed epic.paused report does not change the recheck outcome.
func TestPublishRecheckSurvivesFailedReport(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.rep.fail = map[string]error{"epic.paused": errors.New("bus down")}
	w.check.results = []CheckResult{{Pass: false, Output: "FAIL x"}}
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
	if k, r := peKind(err); !errors.Is(err, ErrRecheck) || k != KindRecheck || r {
		t.Fatalf("err = %v", err)
	}
}

// A group or epic branch equal to the resolved base branch is refused before anything is written.
func TestBaseBranchEqualRefused(t *testing.T) {
	for _, base := range []string{"komodo/ep-1", "komodo/ep-1-tg-1.1"} {
		w := newWorld(t)
		h := w.handler()
		h.Config.BaseBranch = base
		g := w.newGroup("tg-1.1")
		w.work(g, "a.txt", "a")
		tip := w.remoteTip("komodo/ep-1")
		_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
		if k, _ := peKind(err); !errors.Is(err, ErrCriticalRef) || k != KindCriticalRef {
			t.Fatalf("base %s: err = %v", base, err)
		}
		if w.remoteTip("komodo/ep-1") != tip || len(w.gh.calls()) != 0 {
			t.Fatalf("base %s: something was written", base)
		}
	}
}

// Save hashes the worktree once, and Publish reads the landings once.
func TestNoRepeatedWork(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	n := w.countCmd("write-tree")
	if res, err := h.Save(w.ctx, saveReq(g, "tsk-1")); err != nil || !res.Saved {
		t.Fatalf("save = %+v, %v", res, err)
	}
	if got := w.countCmd("write-tree") - n; got != 1 {
		t.Fatalf("Save hashed the worktree %d times, want 1", got)
	}
	w.work(g, "b.txt", "b")
	before := w.countCmd("--first-parent")
	if _, err := h.Publish(w.ctx, w.approval(g, "tg-1.1")); err != nil {
		t.Fatal(err)
	}
	if got := w.countCmd("--first-parent") - before; got != 1 {
		t.Fatalf("Publish read the landings %d times, want 1", got)
	}
}
