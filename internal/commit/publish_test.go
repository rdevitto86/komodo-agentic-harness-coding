package commit

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// bindID converts a plain string to a bus id.
func bindID(s string) bus.ID { return bus.ID(s) }

// peKind returns the kind of a PublishError, or "" when err is not one.
func peKind(err error) (string, bool) {
	var pe *PublishError
	if errors.As(err, &pe) {
		return pe.Kind, pe.Retryable
	}
	return "", false
}

// Verdicts that name different trees write nothing.
func TestPublishVerdictsDisagree(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	h := w.handler()
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	a.Reviewer.Tree = strings.Repeat("a", 40)
	before := git(t, g.wt, "rev-parse", "HEAD")
	_, err := h.Publish(w.ctx, a)
	if k, _ := peKind(err); !errors.Is(err, ErrNotApproved) || k != KindNotApproved {
		t.Fatalf("err = %v", err)
	}
	if git(t, g.wt, "rev-parse", "HEAD") != before || w.remoteTip("komodo/ep-1") == "" || len(w.gh.calls()) != 0 ||
		len(w.check.calls) != 0 {
		t.Fatal("something was written or run")
	}
}

// A worktree edited after approval fails with ErrNotApproved and makes no commit.
func TestPublishWorktreeEditedAfterApproval(t *testing.T) {
	w := newWorld(t)
	g := w.newGroup("tg-1.1")
	h := w.handler()
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	w.work(g, "a.txt", "edited")
	before := git(t, g.wt, "rev-parse", "HEAD")
	_, err := h.Publish(w.ctx, a)
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v", err)
	}
	if git(t, g.wt, "rev-parse", "HEAD") != before {
		t.Fatal("a commit was made")
	}
	if len(w.rep.subtypes()) != 0 || len(w.clean.calls) != 0 {
		t.Fatal("reports or cleanup happened")
	}
}

// 3 groups of 2 commits each land as 3 commits on the local epic branch with no push, no pull request call and no change on
// the remote; the last landing says it is last.
func TestPublishThreeGroupsLandLocallyWithoutAPush(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	groups := []string{"tg-1.1", "tg-1.2", "tg-1.3"}
	remote := w.remoteTip("komodo/ep-1")
	before := w.localTip()

	for i, label := range groups {
		_, r := w.land(h, label, groups...)
		if r.Last != (i == 2) || r.Branch != "komodo/ep-1" || r.Head != w.localTip() || r.Landed != r.Head {
			t.Fatalf("%s result = %+v", label, r)
		}
		if got := git(t, w.clone, "rev-list", "--count", before+"..komodo/ep-1"); got != string(rune('1'+i)) {
			t.Fatalf("after %s the epic gained %s commits, want %d", label, got, i+1)
		}
		if w.pushes() != 0 || len(w.gh.calls()) != 0 || w.remoteTip("komodo/ep-1") != remote {
			t.Fatalf("after %s: pushes %d, forge calls %v, remote moved %v",
				label, w.pushes(), w.gh.calls(), w.remoteTip("komodo/ep-1") != remote)
		}
	}

	if got := git(t, w.clone, "rev-list", "--merges", "--count", before+"..komodo/ep-1"); got != "0" {
		t.Fatalf("the epic gained %s merge commits, want 0", got)
	}
	if got := git(t, w.clone, "log", "--format=%s", before+"..komodo/ep-1"); got !=
		"tg-1.3: Goal of tg-1.3\ntg-1.2: Goal of tg-1.2\ntg-1.1: Goal of tg-1.1" {
		t.Fatalf("epic subjects = %q", got)
	}
	if tr := git(t, w.clone, "log", "--format=%(trailers:only)", before+"..komodo/ep-1"); strings.TrimSpace(tr) != "" {
		t.Fatalf("trailers found: %q", tr)
	}
	if got := w.rep.subtypes(); strings.Join(got, ",") != "publish.done,publish.done,publish.done" {
		t.Fatalf("reports = %v", got)
	}
	if len(w.clean.calls) != 3 || len(w.clean.epics) != 0 {
		t.Fatalf("cleanup: %d group calls, %d epic calls", len(w.clean.calls), len(w.clean.epics))
	}
	if c := w.clean.calls[2]; c.Into != "komodo/ep-1" || c.Landed != w.localTip() ||
		c.Tip == "" {
		t.Fatalf("published = %+v", c)
	}
	if git(t, w.clone, "rev-parse", w.clean.calls[0].Tip+"^{tree}") == "" {
		t.Fatal("group tip is gone")
	}
}

// A second Publish with the same approval lands nothing again, pushes nothing and calls Complete again.
func TestPublishRepeatIsIdempotent(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1", "tg-1.2")
	a, r1 := w.land(h, "tg-1.2", "tg-1.1", "tg-1.2")
	tip := w.localTip()
	for i := 0; i < 2; i++ {
		r, err := h.Publish(w.ctx, a)
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if r.Landed != r1.Landed || r.Head != tip || !r.Last {
			t.Fatalf("repeat %d = %+v, want landed %s", i, r, r1.Landed)
		}
	}
	if w.localTip() != tip || w.pushes() != 0 || len(w.gh.calls()) != 0 {
		t.Fatal("a repeat moved the epic or reached the remote")
	}
	if len(w.clean.calls) != 4 {
		t.Fatalf("Complete called %d times, want 4", len(w.clean.calls))
	}
}

// A stale fence read just before the landing refuses it and leaves the epic branch as it was.
func TestPublishStaleFenceBeforeLanding(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	tip := w.localTip()
	w.leases.onRead = func(n int) {
		if n == 2 {
			w.leases.bump(g.id, 9)
		}
	}
	_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
	if k, r := peKind(err); !errors.Is(err, ErrLeaseLost) || k != KindLeaseLost || r {
		t.Fatalf("err = %v", err)
	}
	if w.localTip() != tip {
		t.Fatal("epic moved")
	}
}

// A seeded conflict returns ErrConflict on the group branch and leaves the epic branch unchanged.
func TestPublishConflict(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "README.md", "group\n")
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "komodo/ep-1")
	write(t, other, "README.md", "epic\n")
	git(t, other, "commit", "-am", "epic edit")
	git(t, other, "push", "origin", "komodo/ep-1")
	remote := w.remoteTip("komodo/ep-1")
	_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
	if k, r := peKind(err); !errors.Is(err, ErrConflict) || k != KindMergeConfl || r {
		t.Fatalf("err = %v", err)
	}
	if w.remoteTip("komodo/ep-1") != remote || git(t, w.clone, "log", "-1", "--format=%s", "komodo/ep-1") != "epic edit" {
		t.Fatal("epic branch changed beyond taking the remote's commit")
	}
	if st := git(t, g.wt, "status", "--porcelain"); st != "" {
		t.Fatalf("group worktree left dirty: %q", st)
	}
	if len(w.clean.calls) != 0 {
		t.Fatal("cleanup reached")
	}
}

// A failing recheck pauses the epic, reports epic.paused, holds a second group, and a passing recheck from the paused group
// clears the pause.
func TestPublishRecheckPausesAndResumes(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	// the first turn on an epic rechecks before the merge (passes), then the recheck after the landing fails
	w.check.results = []CheckResult{{Pass: true}, {Pass: false, Output: "FAIL internal/lease 2 tests failed"}}
	g1 := w.newGroup("tg-1.1")
	w.work(g1, "a.txt", "a")
	a1 := w.approval(g1, "tg-1.1", "tg-1.2")
	_, err := h.Publish(w.ctx, a1)
	if k, r := peKind(err); !errors.Is(err, ErrRecheck) || k != KindRecheck || r {
		t.Fatalf("err = %v", err)
	}
	if got := w.rep.subtypes(); strings.Join(got, ",") != "epic.paused" {
		t.Fatalf("reports = %v", got)
	}
	if p, ok := w.rep.list[0].data.(bus.EpicPaused); !ok || p.Epic != "ep-1" || p.Group != "tg-1.1" ||
		p.Output != "FAIL internal/lease 2 tests failed" {
		t.Fatalf("paused data = %+v", w.rep.list[0].data)
	}
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	a2 := w.approval(g2, "tg-1.1", "tg-1.2")
	tipAfterG1 := w.localTip()
	done := make(chan error, 1)
	go func() { _, err := h.Publish(w.ctx, a2); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("second group landed while the epic was paused: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if w.localTip() != tipAfterG1 {
		t.Fatal("epic moved while paused")
	}
	if _, err := h.Publish(w.ctx, a1); err != nil {
		t.Fatalf("paused group's repeat: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second group: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second group never landed after the pause cleared")
	}
	var seen []string
	for _, s := range w.rep.subtypes() {
		if strings.HasPrefix(s, "epic.") {
			seen = append(seen, s)
		}
	}
	if strings.Join(seen, ",") != "epic.paused,epic.resumed" {
		t.Fatalf("epic reports = %v", seen)
	}
}

// After an engine restart the first landing on an epic runs the recheck before the merge.
func TestPublishFirstTurnRechecksBeforeMerge(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g1 := w.newGroup("tg-1.1")
	w.work(g1, "a.txt", "a")
	if _, err := h.Publish(w.ctx, w.approval(g1, "tg-1.1", "tg-1.2")); err != nil {
		t.Fatal(err)
	}
	h2 := w.handler()
	w.check.results = []CheckResult{{Pass: false, Output: "forgotten pause"}}
	w.check.calls = nil
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	tip := w.localTip()
	_, err := h2.Publish(w.ctx, w.approval(g2, "tg-1.1", "tg-1.2"))
	if !errors.Is(err, ErrRecheck) {
		t.Fatalf("err = %v", err)
	}
	if len(w.check.calls) != 1 || w.localTip() != tip {
		t.Fatalf("recheck calls = %v; epic tip moved = %v", w.check.calls, w.localTip() != tip)
	}
	if ls, _ := w.git.Landed(w.ctx, w.clone, "origin/main", "komodo/ep-1"); len(ls) != 1 {
		t.Fatalf("landings = %+v", ls)
	}
}

// A cleanup error comes back unchanged and a repeat reaches Complete again.
func TestPublishCleanupErrorReturned(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	boom := errors.New("cleanup boom")
	w.clean.err = boom
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	if _, err := h.Publish(w.ctx, a); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	w.clean.err = nil
	if _, err := h.Publish(w.ctx, a); err != nil {
		t.Fatal(err)
	}
	if len(w.clean.calls) != 2 {
		t.Fatalf("Complete called %d times", len(w.clean.calls))
	}
	if got := git(t, w.clone, "rev-list", "--count", "origin/main..komodo/ep-1"); got != "1" {
		t.Fatalf("the repeat landed again: %s commits", got)
	}
}

// A base branch the remote lacks fails with kind base_missing; empty settings use the remote's default branch and origin.
func TestPublishBaseBranch(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	h.Config.BaseBranch = "nope"
	_, err := h.Publish(w.ctx, a)
	if k, r := peKind(err); k != KindBaseMissing || r {
		t.Fatalf("err = %v", err)
	}
	h.Config = Settings{}
	if _, err := h.Publish(w.ctx, a); err != nil {
		t.Fatalf("default settings: %v", err)
	}
}

// Live settings win over Config, and each Publish reads them afresh: a changed base applies to the next call.
func TestPublishReadsLiveSettings(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	live := Settings{BaseBranch: "nope", Remote: "origin"}
	h.Live = func() Settings { return live }
	_, err := h.Publish(w.ctx, a)
	if k, r := peKind(err); k != KindBaseMissing || r {
		t.Fatalf("err = %v", err)
	}
	live = Settings{BaseBranch: "main", Remote: "origin"}
	if _, err := h.Publish(w.ctx, a); err != nil {
		t.Fatalf("changed settings: %v", err)
	}
}

// Publish on a worktree with a branch checked out returns ErrNotApproved and lands nothing.
func TestPublishWorktreeWithBranch(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	git(t, g.wt, "checkout", "-q", "-b", "komodo/stray")
	before := w.localTip()
	_, err := h.Publish(w.ctx, a)
	if k, _ := peKind(err); !errors.Is(err, ErrNotApproved) || k != KindNotApproved {
		t.Fatalf("err = %v", err)
	}
	if w.localTip() != before || len(w.clean.calls) != 0 {
		t.Fatal("something landed")
	}
}

// Two groups of one epic landing at once land one after the other, in the order they arrived.
func TestPublishConcurrentGroupsLandInArrivalOrder(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	gA, gB := w.newGroup("tg-1.1"), w.newGroup("tg-1.2")
	w.work(gA, "a.txt", "a")
	w.work(gB, "b.txt", "b")
	aA, aB := w.approval(gA, "tg-1.1", "tg-1.2"), w.approval(gB, "tg-1.1", "tg-1.2")
	hold, err := h.Epics.Enter(w.ctx, "repo/ep-1", "holder")
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	// B arrives first, A second.
	go func() { _, err := h.Publish(w.ctx, aB); errs <- err }()
	waitQueued(t, h.Epics, "repo/ep-1", 1)
	go func() { _, err := h.Publish(w.ctx, aA); errs <- err }()
	waitQueued(t, h.Epics, "repo/ep-1", 2)
	hold.Release()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	ls, err := w.git.Landed(w.ctx, w.clone, "origin/main", "komodo/ep-1")
	if err != nil || len(ls) != 2 {
		t.Fatalf("landings = %+v, %v", ls, err)
	}
	if ls[1].Label != "tg-1.2" || ls[0].Label != "tg-1.1" {
		t.Fatalf("landing order (oldest last) = %s, %s; want tg-1.2 then tg-1.1", ls[1].Label, ls[0].Label)
	}
}

// A group whose recheck failed returns to Build; its fixed second approval lands a new commit that holds the fix, and the
// epic's pull request body names the group once.
func TestPublishFixAfterAFailedRecheckLandsAgain(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.check.results = []CheckResult{{Pass: true}, {Pass: false, Output: "FAIL"}, {Pass: true}}
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "broken")
	if _, err := h.Publish(w.ctx, w.approval(g, "tg-1.1")); !errors.Is(err, ErrRecheck) {
		t.Fatalf("first publish err = %v", err)
	}
	before := w.localTip()

	w.work(g, "a.txt", "fixed")
	a := w.approval(g, "tg-1.1")
	res, err := h.Publish(w.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if w.localTip() == before || res.Landed != w.localTip() || !res.Last {
		t.Fatalf("result = %+v, epic tip %s, before %s", res, w.localTip(), before)
	}
	if got := git(t, w.clone, "show", "refs/heads/komodo/ep-1:a.txt"); got != "fixed" {
		t.Fatalf("epic branch holds %q, want the fix", got)
	}

	again, err := h.Publish(w.ctx, a)
	if err != nil || again.Landed != res.Landed || w.localTip() != res.Landed {
		t.Fatalf("a repeat of the fixed approval = %+v, %v", again, err)
	}

	if _, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1")); err != nil {
		t.Fatal(err)
	}
	if body := w.gh.file("create-body.txt"); strings.Count(body, "## Summary") != 2 {
		t.Fatalf("the body holds the group more than once:\n%s", body)
	}
}

// A remote with no epic branch yet, as after a local task-list commit, still allows the landing; nothing is pushed.
func TestPublishWithNoRemoteEpicBranch(t *testing.T) {
	w := newWorld(t)
	git(t, w.clone, "push", "origin", "--delete", "komodo/ep-1")
	git(t, w.clone, "update-ref", "-d", "refs/remotes/origin/komodo/ep-1")
	h := w.handler()
	_, r := w.land(h, "tg-1.1", "tg-1.1")
	if !r.Last || w.pushes() != 0 || r.Head != w.localTip() {
		t.Fatalf("result = %+v, pushes %d", r, w.pushes())
	}
	if out, err := execOut(w.remote, "rev-parse", "--verify", "refs/heads/komodo/ep-1"); err == nil {
		t.Fatalf("the remote has the epic branch: %s", out)
	}
}
