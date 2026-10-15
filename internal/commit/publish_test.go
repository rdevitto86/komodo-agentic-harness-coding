package commit

import (
	"context"
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
	before := git(t, w.clone, "rev-parse", g.branch)
	_, err := h.Publish(w.ctx, a)
	if k, _ := peKind(err); !errors.Is(err, ErrNotApproved) || k != KindNotApproved {
		t.Fatalf("err = %v", err)
	}
	if git(t, w.clone, "rev-parse", g.branch) != before || w.remoteTip("komodo/ep-1") == "" || len(w.gh.calls()) != 0 || len(w.check.calls) != 0 {
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
	before := git(t, w.clone, "rev-parse", g.branch)
	_, err := h.Publish(w.ctx, a)
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("err = %v", err)
	}
	if git(t, w.clone, "rev-parse", g.branch) != before {
		t.Fatal("a commit was made")
	}
	if len(w.rep.subtypes()) != 0 || len(w.clean.calls) != 0 {
		t.Fatal("reports or cleanup happened")
	}
}

// The first group of 2 opens no pull request; the second opens exactly one, from the epic branch into the base, titled with the group count.
func TestPublishTwoGroupsOpensOnePullRequest(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g1 := w.newGroup("tg-1.1")
	w.work(g1, "a.txt", "a")
	r1, err := h.Publish(w.ctx, w.approval(g1, "tg-1.1", "tg-1.2"))
	if err != nil {
		t.Fatal(err)
	}
	if r1.Last || r1.PRURL != "" || r1.PRNew || r1.Branch != "komodo/ep-1" {
		t.Fatalf("first = %+v", r1)
	}
	if w.gh.count("pr create") != 0 {
		t.Fatal("pull request opened after the first group")
	}
	if r1.Head != w.remoteTip("komodo/ep-1") {
		t.Fatalf("head %s, remote %s", r1.Head, w.remoteTip("komodo/ep-1"))
	}
	if n := w.clean.calls[0]; n.EpicDone || n.Tip == "" || n.Contains != "origin/komodo/ep-1" || n.Branch != g1.branch || n.Remote != "origin" {
		t.Fatalf("published = %+v", n)
	}
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	r2, err := h.Publish(w.ctx, w.approval(g2, "tg-1.1", "tg-1.2"))
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Last || !r2.PRNew || r2.PRURL != "https://github.com/acme/shop/pull/41" {
		t.Fatalf("second = %+v", r2)
	}
	if w.gh.count("pr create") != 1 {
		t.Fatalf("calls = %v", w.gh.calls())
	}
	var create string
	for _, c := range w.gh.calls() {
		if strings.HasPrefix(c, "pr create") {
			create = c
		}
	}
	for _, want := range []string{"--base main", "--head komodo/ep-1", "--title ep-1: 2 task groups", "--label komodo"} {
		if !strings.Contains(create, want) {
			t.Fatalf("create %q lacks %q", create, want)
		}
	}
	if strings.Contains(create, "--draft") {
		t.Fatal("pull request opened as a draft")
	}
	body := w.gh.file("create-body.txt")
	if !strings.Contains(body, "Goal") && !strings.Contains(body, "14 tests pass") || !strings.Contains(body, "tsk-tg-1.1") || !strings.Contains(body, "tsk-tg-1.2") {
		t.Fatalf("body lacks both group sections:\n%s", body)
	}
	if got := w.rep.subtypes(); strings.Join(got, ",") != "publish.done,publish.done,pr.opened" {
		t.Fatalf("reports = %v", got)
	}
	if last := w.clean.calls[1]; !last.EpicDone || last.PRURL != r2.PRURL {
		t.Fatalf("published = %+v", last)
	}
	if out := git(t, w.remote, "branch", "--list", "komodo/ep-1-*"); out != "" {
		t.Fatalf("group branches were pushed: %s", out)
	}
	if tr := git(t, w.remote, "log", "--format=%(trailers:only)", "komodo/ep-1"); strings.TrimSpace(tr) != "" {
		t.Fatalf("trailers found: %q", tr)
	}
}

// A second Publish with the same approval opens no second pull request and calls Complete again.
func TestPublishRepeatIsIdempotent(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g1 := w.newGroup("tg-1.1")
	w.work(g1, "a.txt", "a")
	if _, err := h.Publish(w.ctx, w.approval(g1, "tg-1.1", "tg-1.2")); err != nil {
		t.Fatal(err)
	}
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	a := w.approval(g2, "tg-1.1", "tg-1.2")
	r1, err := h.Publish(w.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	tip := w.remoteTip("komodo/ep-1")
	for i := 0; i < 2; i++ {
		r, err := h.Publish(w.ctx, a)
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if r.PRNew || r.PRURL != r1.PRURL || r.Head != tip {
			t.Fatalf("repeat %d = %+v", i, r)
		}
	}
	if w.gh.count("pr create") != 1 || w.gh.count("pr edit") != 2 {
		t.Fatalf("calls = %v", w.gh.calls())
	}
	if w.remoteTip("komodo/ep-1") != tip {
		t.Fatal("repeat moved the remote")
	}
	if len(w.clean.calls) != 4 {
		t.Fatalf("Complete called %d times, want 4", len(w.clean.calls))
	}
}

// A stale fence read just before the push refuses the push.
func TestPublishStaleFenceBeforePush(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	tip := w.remoteTip("komodo/ep-1")
	w.leases.onRead = func(n int) {
		if n == 2 {
			w.leases.bump(g.id, 9)
		}
	}
	_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
	if k, r := peKind(err); !errors.Is(err, ErrLeaseLost) || k != KindLeaseLost || r {
		t.Fatalf("err = %v", err)
	}
	if w.remoteTip("komodo/ep-1") != tip {
		t.Fatal("remote moved")
	}
}

// A remote that gained commits after the fetch rejects the push; nothing is forced and the turn is freed.
func TestPublishPushRejected(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "komodo/ep-1")
	write(t, other, "x.txt", "x")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "extra")
	w.leases.onRead = func(n int) {
		if n == 2 {
			git(t, other, "push", "origin", "komodo/ep-1")
		}
	}
	_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
	if k, r := peKind(err); !errors.Is(err, ErrPushRejected) || k != KindPushRejected || r {
		t.Fatalf("err = %v", err)
	}
	if got, want := w.remoteTip("komodo/ep-1"), git(t, other, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote = %s, want the other clone's %s", got, want)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Second)
	defer cancel()
	turn, err := h.Epics.Enter(ctx, "repo/ep-1", "probe")
	if err != nil {
		t.Fatalf("turn still held: %v", err)
	}
	turn.Release()
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
	tip := w.remoteTip("komodo/ep-1")
	_, err := h.Publish(w.ctx, w.approval(g, "tg-1.1"))
	if k, r := peKind(err); !errors.Is(err, ErrConflict) || k != KindMergeConfl || r {
		t.Fatalf("err = %v", err)
	}
	if w.remoteTip("komodo/ep-1") != tip {
		t.Fatal("epic branch changed")
	}
	if st := git(t, g.wt, "status", "--porcelain"); st != "" {
		t.Fatalf("group worktree left dirty: %q", st)
	}
	if len(w.clean.calls) != 0 {
		t.Fatal("cleanup reached")
	}
}

// A failing recheck pauses the epic, reports epic.paused, holds a second group, and a passing recheck from the paused group clears the pause.
func TestPublishRecheckPausesAndResumes(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	// the first turn on an epic rechecks before the merge (passes), then the recheck after the push fails
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
	if p, ok := w.rep.list[0].data.(bus.EpicPaused); !ok || p.Epic != "ep-1" || p.Group != "tg-1.1" || p.Output != "FAIL internal/lease 2 tests failed" {
		t.Fatalf("paused data = %+v", w.rep.list[0].data)
	}
	g2 := w.newGroup("tg-1.2")
	w.work(g2, "b.txt", "b")
	a2 := w.approval(g2, "tg-1.1", "tg-1.2")
	tipAfterG1 := w.remoteTip("komodo/ep-1")
	done := make(chan error, 1)
	go func() { _, err := h.Publish(w.ctx, a2); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("second group landed while the epic was paused: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if w.remoteTip("komodo/ep-1") != tipAfterG1 {
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
	tip := w.remoteTip("komodo/ep-1")
	_, err := h2.Publish(w.ctx, w.approval(g2, "tg-1.1", "tg-1.2"))
	if !errors.Is(err, ErrRecheck) {
		t.Fatalf("err = %v", err)
	}
	if len(w.check.calls) != 1 || w.remoteTip("komodo/ep-1") != tip {
		t.Fatalf("recheck calls = %v; epic tip moved = %v", w.check.calls, w.remoteTip("komodo/ep-1") != tip)
	}
	if ls, _ := w.git.Landed(w.ctx, w.clone, "origin/main", "origin/komodo/ep-1"); len(ls) != 1 {
		t.Fatalf("landings = %+v", ls)
	}
}

// flakyForge fails OpenPR a set number of times, then defers to the real forge.
type flakyForge struct {
	Forge
	fail int
}

// OpenPR fails while fail is positive.
func (f *flakyForge) OpenPR(ctx context.Context, base, head, title, body, label string) (PR, error) {
	if f.fail > 0 {
		f.fail--
		return PR{}, errors.New("gh: network down")
	}
	return f.Forge.OpenPR(ctx, base, head, title, body, label)
}

// A crash between the push and the pull request opens the pull request on the repeat.
func TestPublishCrashBetweenPushAndPullRequest(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	h.Forge = &flakyForge{Forge: NewGH(w.clone), fail: 1}
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	_, err := h.Publish(w.ctx, a)
	if k, r := peKind(err); k != KindForge || !r {
		t.Fatalf("err = %v", err)
	}
	tip := w.remoteTip("komodo/ep-1")
	if len(w.clean.calls) != 0 {
		t.Fatal("cleanup ran before the pull request")
	}
	r, err := h.Publish(w.ctx, a)
	if err != nil || !r.PRNew || r.Head != tip {
		t.Fatalf("repeat = %+v, %v", r, err)
	}
	if w.gh.count("pr create") != 1 || len(w.clean.calls) != 1 {
		t.Fatalf("calls = %v, cleanup %d", w.gh.calls(), len(w.clean.calls))
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
	if w.gh.count("pr create") != 1 || !strings.Contains(strings.Join(w.gh.calls(), "\n"), "--base main") {
		t.Fatalf("calls = %v", w.gh.calls())
	}
}

// Publish refuses a group branch outside komodo/ before any git runs.
func TestPublishCriticalBranch(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	g := w.newGroup("tg-1.1")
	w.work(g, "a.txt", "a")
	a := w.approval(g, "tg-1.1")
	a.Branch = "main"
	n := len(w.traced)
	_, err := h.Publish(w.ctx, a)
	if k, _ := peKind(err); !errors.Is(err, ErrCriticalRef) || k != KindCriticalRef {
		t.Fatalf("err = %v", err)
	}
	if len(w.traced) != n {
		t.Fatalf("git ran: %v", w.traced[n:])
	}
}
