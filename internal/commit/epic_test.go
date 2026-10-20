package commit

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

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

// 3 groups land locally, then one PushEpic makes 1 push and opens 1 pull request titled with the group count; the remote
// holds the 3 landing commits and the group branches stay off it.
func TestPushEpicAfterThreeGroupsPushesOnceAndOpensOnePullRequest(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	groups := []string{"tg-1.1", "tg-1.2", "tg-1.3"}
	for _, label := range groups {
		w.land(h, label, groups...)
	}
	if w.pushes() != 0 || len(w.gh.calls()) != 0 {
		t.Fatalf("before the epic push: pushes %d, forge calls %v", w.pushes(), w.gh.calls())
	}

	res, err := h.PushEpic(w.ctx, w.epicReq(groups...))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || !res.Last || !res.PRNew || res.PRURL != "https://github.com/acme/shop/pull/41" ||
		res.Branch != "komodo/ep-1" || res.Head != w.localTip() ||
		strings.Join(res.Landed, ",") != strings.Join(groups, ",") {
		t.Fatalf("result = %+v", res)
	}
	if w.pushes() != 1 || w.gh.count("pr create") != 1 || w.gh.count("pr edit") != 0 {
		t.Fatalf("pushes %d, forge calls %v", w.pushes(), w.gh.calls())
	}
	if w.remoteTip("komodo/ep-1") != w.localTip() {
		t.Fatal("the remote does not hold the local epic tip")
	}
	if got := git(t, w.remote, "rev-list", "--count", "main..komodo/ep-1"); got != "3" {
		t.Fatalf("the remote epic holds %s commits beyond main, want 3", got)
	}
	if out := git(t, w.remote, "branch", "--list", "komodo/ep-1-*"); out != "" {
		t.Fatalf("group branches were pushed: %s", out)
	}

	var create string
	for _, c := range w.gh.calls() {
		if strings.HasPrefix(c, "pr create") {
			create = c
		}
	}
	for _, want := range []string{"--base main", "--head komodo/ep-1", "--title ep-1: 3 task groups", "--label komodo"} {
		if !strings.Contains(create, want) {
			t.Fatalf("create %q lacks %q", create, want)
		}
	}
	if strings.Contains(create, "--draft") {
		t.Fatal("pull request opened as a draft")
	}
	body := w.gh.file("create-body.txt")
	for _, want := range []string{"tsk-tg-1.1", "tsk-tg-1.2", "tsk-tg-1.3", "14 tests pass"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	if len(w.clean.epics) != 1 || w.clean.epics[0].PRURL != res.PRURL || w.clean.epics[0].Contains != "origin/komodo/ep-1" ||
		w.clean.epics[0].Remote != "origin" || w.clean.epics[0].Epic != "ep-1" {
		t.Fatalf("epic cleanup = %+v", w.clean.epics)
	}
}

// A run that stops with 2 of 3 groups landed pushes the branch once and opens no pull request; the run that lands the third
// pushes once more and opens the pull request.
func TestPushEpicEarlyStopThenResume(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	groups := []string{"tg-1.1", "tg-1.2", "tg-1.3"}
	w.land(h, "tg-1.1", groups...)
	w.land(h, "tg-1.2", groups...)

	res, err := h.PushEpic(w.ctx, w.epicReq(groups...))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || res.Last || res.PRURL != "" || strings.Join(res.Landed, ",") != "tg-1.1,tg-1.2" {
		t.Fatalf("early stop result = %+v", res)
	}
	if w.pushes() != 1 || len(w.gh.calls()) != 0 || len(w.clean.epics) != 0 {
		t.Fatalf("pushes %d, forge calls %v, epic cleanups %d", w.pushes(), w.gh.calls(), len(w.clean.epics))
	}
	if w.remoteTip("komodo/ep-1") != w.localTip() {
		t.Fatal("the remote does not hold the 2 landed groups")
	}

	h2 := w.handler()
	w.land(h2, "tg-1.3", groups...)
	if w.pushes() != 1 {
		t.Fatalf("the third landing pushed: %d pushes", w.pushes())
	}

	res, err = h2.PushEpic(w.ctx, w.epicReq(groups...))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || !res.Last || !res.PRNew || w.pushes() != 2 || w.gh.count("pr create") != 1 {
		t.Fatalf("resume result = %+v; pushes %d, forge calls %v", res, w.pushes(), w.gh.calls())
	}
	if w.remoteTip("komodo/ep-1") != w.localTip() || len(w.clean.epics) != 1 {
		t.Fatal("the remote lacks the third group or the epic was not cleaned up")
	}
}

// Landed work a crash left in the local epic branch is pushed by the next run's end, even when the landing worktree is gone.
func TestPushEpicAfterACrashLosesNothing(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	groups := []string{"tg-1.1", "tg-1.2"}
	w.land(h, "tg-1.1", groups...)
	w.land(h, "tg-1.2", groups...)
	landing := git(t, w.clone, "worktree", "list", "--porcelain")
	if !strings.Contains(landing, "komodo/ep-1") {
		t.Fatalf("no landing worktree:\n%s", landing)
	}
	if err := w.git.RemoveWorktree(w.ctx, w.clone, filepath.Join(w.root, "wt", "ep-1")); err != nil {
		t.Fatal(err)
	}
	tip := w.localTip()
	if w.remoteTip("komodo/ep-1") == tip {
		t.Fatal("the crash scenario needs unpushed work")
	}
	worktrees := countLines(git(t, w.clone, "worktree", "list"))

	res, err := w.handler().PushEpic(w.ctx, w.epicReq(groups...))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || !res.Last || w.remoteTip("komodo/ep-1") != tip || w.gh.count("pr create") != 1 {
		t.Fatalf("result = %+v; remote %s, local %s", res, w.remoteTip("komodo/ep-1"), tip)
	}
	if n := countLines(git(t, w.clone, "worktree", "list")); n != worktrees {
		t.Fatalf("the temporary worktree was left behind: %d worktrees, want %d", n, worktrees)
	}
}

// A second PushEpic with nothing new pushes nothing, edits no pull request and opens none.
func TestPushEpicRepeatChangesNothing(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1")
	first, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil {
		t.Fatal(err)
	}

	again, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Pushed || again.PRNew || again.PRURL != first.PRURL || again.Head != first.Head {
		t.Fatalf("repeat = %+v, first = %+v", again, first)
	}
	if w.pushes() != 1 || w.gh.count("pr create") != 1 || w.gh.count("pr edit") != 0 {
		t.Fatalf("pushes %d, forge calls %v", w.pushes(), w.gh.calls())
	}
}

// A push that changes the branch after the pull request opened refreshes the pull request once and opens no second one.
func TestPushEpicRefreshesAnOpenPullRequest(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1")
	if _, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1")); err != nil {
		t.Fatal(err)
	}
	land := filepath.Join(w.root, "wt", "ep-1")
	git(t, land, "commit", "--allow-empty", "-m", "ep-1: later work")

	res, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || res.PRNew || res.PRURL == "" || w.gh.count("pr create") != 1 || w.gh.count("pr edit") != 1 {
		t.Fatalf("result = %+v; forge calls %v", res, w.gh.calls())
	}
	if w.pushes() != 2 {
		t.Fatalf("pushes = %d, want 2", w.pushes())
	}
}

// Nothing landed means nothing to push: the plan commit stays local and no forge call is made.
func TestPushEpicWithNoLandingPushesNothing(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	res, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed || res.Last || len(res.Landed) != 0 || w.pushes() != 0 || len(w.gh.calls()) != 0 {
		t.Fatalf("result = %+v; pushes %d, forge calls %v", res, w.pushes(), w.gh.calls())
	}
}

// Commits another clone pushed to the epic branch are merged first, so the push is a fast-forward and loses neither side.
func TestPushEpicMergesWhatTheRemoteGained(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1")
	other := filepath.Join(w.root, "other")
	git(t, w.root, "clone", w.remote, other)
	git(t, other, "checkout", "komodo/ep-1")
	write(t, other, "x.txt", "x")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "from another clone")
	git(t, other, "push", "origin", "komodo/ep-1")

	res, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || !res.Last || w.remoteTip("komodo/ep-1") != w.localTip() {
		t.Fatalf("result = %+v", res)
	}
	tree := w.remoteTip("komodo/ep-1") + ":"
	for _, f := range []string{"x.txt", "tg-1.1-a.txt"} {
		if out, err := execOut(w.remote, "cat-file", "-e", tree+f); err != nil {
			t.Fatalf("remote lacks %s: %s", f, out)
		}
	}
}

// A paused epic is pushed, so the landed work is safe, but no pull request opens until a passing recheck clears the pause.
func TestPushEpicPausedOpensNoPullRequest(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1")
	turn, err := h.Epics.Enter(w.ctx, "repo/ep-1", "tg-1.1")
	if err != nil {
		t.Fatal(err)
	}
	turn.Finish(false)

	res, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || !res.Last || res.PRURL != "" || len(w.gh.calls()) != 0 || len(w.clean.epics) != 0 {
		t.Fatalf("result = %+v; forge calls %v", res, w.gh.calls())
	}
}

// A forge failure after the push is retryable; the repeat opens the pull request without pushing again.
func TestPushEpicForgeFailureThenRepeat(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	h.Forge = &flakyForge{Forge: NewGH(w.clone), fail: 1}
	w.land(h, "tg-1.1", "tg-1.1")

	_, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if k, r := peKind(err); k != KindForge || !r {
		t.Fatalf("err = %v", err)
	}
	if len(w.clean.epics) != 0 || w.pushes() != 1 {
		t.Fatalf("epic cleanups %d, pushes %d", len(w.clean.epics), w.pushes())
	}

	res, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if err != nil || res.Pushed || !res.PRNew || w.pushes() != 1 || w.gh.count("pr create") != 1 {
		t.Fatalf("repeat = %+v, %v; pushes %d", res, err, w.pushes())
	}
	if len(w.clean.epics) != 1 {
		t.Fatalf("epic cleanups = %d, want 1", len(w.clean.epics))
	}
}

// A cleanup error after the pull request comes back unchanged, and a repeat reaches the epic cleanup again.
func TestPushEpicCleanupErrorReturned(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1")
	boom := errors.New("cleanup boom")
	w.clean.err = boom
	if _, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1")); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	w.clean.err = nil
	if _, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1")); err != nil {
		t.Fatal(err)
	}
	if len(w.clean.epics) != 2 || w.gh.count("pr create") != 1 {
		t.Fatalf("epic cleanups %d, forge calls %v", len(w.clean.epics), w.gh.calls())
	}
}

// The base branch, the remote and the label come from the live settings at each call.
func TestPushEpicReadsLiveSettings(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	w.land(h, "tg-1.1", "tg-1.1")
	live := Settings{BaseBranch: "nope", Remote: "origin"}
	h.Live = func() Settings { return live }

	_, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1"))
	if k, r := peKind(err); k != KindBaseMissing || r {
		t.Fatalf("err = %v", err)
	}
	if w.pushes() != 0 {
		t.Fatal("pushed before the base branch resolved")
	}

	live = Settings{BaseBranch: "main", Remote: "origin", PRLabel: "first-run"}
	if _, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1")); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(w.gh.calls(), "\n")
	if !strings.Contains(calls, "--base main") || !strings.Contains(calls, "first-run") {
		t.Fatalf("calls = %v", w.gh.calls())
	}
}

// An epic branch outside komodo/, or equal to the base branch, is refused before any git runs.
func TestPushEpicRefusesCriticalRefs(t *testing.T) {
	w := newWorld(t)
	h := w.handler()
	n := len(w.traced)

	req := w.epicReq("tg-1.1")
	req.Epic = "../main"
	if _, err := h.PushEpic(w.ctx, req); !errors.Is(err, ErrCriticalRef) {
		t.Fatalf("err = %v", err)
	}
	if len(w.traced) != n {
		t.Fatalf("git ran: %v", w.traced[n:])
	}

	h.Config.BaseBranch = "komodo/ep-1"
	if _, err := h.PushEpic(w.ctx, w.epicReq("tg-1.1")); !errors.Is(err, ErrCriticalRef) {
		t.Fatalf("base equal: err = %v", err)
	}
	if w.pushes() != 0 {
		t.Fatal("pushed")
	}
}
