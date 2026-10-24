package engine

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// An epic of 2 groups is pushed once, after the last group is Done and before the run ends, and the push is published as
// epic.pushed and pr.opened.
func TestEpicIsPushedOnceAfterItsLastGroup(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}, {"tg-1.2", "tg-1.1"}})
	var doneAtPush []Stage
	r.commits.result = commit.EpicResult{
		Branch: "komodo/ep-1", Head: "abc", Landed: []string{"tg-1.1", "tg-1.2"}, Pushed: true, Last: true,
		PRURL: "https://github.com/acme/shop/pull/41", PRNew: true,
	}
	r.commits.onPush = func(commit.EpicPush) {
		doneAtPush = []Stage{r.store.get("g-tg-1.1").Stage, r.store.get("g-tg-1.2").Stage}
	}

	if err := r.startRun("run-1", "tg-1.1", "tg-1.2"); err != nil {
		t.Fatal(err)
	}
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

	got := r.commits.pushes()
	if len(got) != 1 || got[0].Epic != "ep-1" || !slices.Equal(got[0].EpicGroups, []string{"tg-1.1", "tg-1.2"}) {
		t.Fatalf("pushes = %+v", got)
	}
	if !slices.Equal(doneAtPush, []Stage{StageDone, StageDone}) {
		t.Fatalf("stages at the push = %v, want both Done", doneAtPush)
	}
	if r.ev.count(bus.TypeRunStatus, "epic.pushed") != 1 || r.ev.count(bus.TypeRunStatus, "pr.opened") != 1 {
		t.Fatalf("epic.pushed %d, pr.opened %d", r.ev.count(bus.TypeRunStatus, "epic.pushed"),
			r.ev.count(bus.TypeRunStatus, "pr.opened"))
	}
}

// The push names the plan's goal, release and every group of the plan, even when the run holds one group.
func TestEpicPushCarriesThePlanFacts(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.plans.facts = PlanFacts{Goal: "Ship it", Release: "V1", GroupGoals: []string{"tg-1.1: First", "tg-1.2: Second"}}

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

	got := r.commits.pushes()
	if len(got) != 1 || got[0].Goal != "Ship it" || got[0].Release != "V1" ||
		!slices.Equal(got[0].GroupGoals, r.plans.facts.GroupGoals) {
		t.Fatalf("pushes = %+v", got)
	}
}

// A plan whose facts cannot be read is not pushed and ends the run with the epic stalled.
func TestEpicPushStallsWhenThePlanFactsCannotBeRead(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.plans.factsErr = errors.New("blueprint is gone")

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

	if n := len(r.commits.pushes()); n != 0 {
		t.Fatalf("%d pushes, want 0", n)
	}
	if r.ev.count(bus.TypeRunFailure, "stalled") != 1 {
		t.Fatal("no stalled fact named the epic")
	}
}

// advanceUntilRunEnds moves the fake clock past each retry wait until the run publishes run-complete.
func advanceUntilRunEnds(t *testing.T, r *rig) {
	t.Helper()

	end := time.Now().Add(20 * time.Second)
	for r.ev.count(bus.TypeRunComplete, "done") == 0 {
		r.clock.Advance(time.Minute)
		time.Sleep(20 * time.Millisecond)

		if time.Now().After(end) {
			t.Fatal("the run never ended")
		}
	}
}

// retryable is a push error the Commit Handler marks as worth repeating.
func retryable(call int) error {
	return &commit.PublishError{Kind: commit.KindPush, Retryable: true, Err: errors.New("network down")}
}

// A retryable push failure is tried again after a wait, and the run ends only after the push succeeds and is published.
func TestRetryablePushFailureIsRetriedBeforeTheRunEnds(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.commits.result = commit.EpicResult{Branch: "komodo/ep-1", Landed: []string{"tg-1.1"}, Pushed: true, Last: true}
	r.commits.errFor = func(call int) error {
		if call <= 2 {
			return retryable(call)
		}
		return nil
	}

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	advanceUntilRunEnds(t, r)

	if n := len(r.commits.pushes()); n != 3 {
		t.Fatalf("%d push tries, want 3", n)
	}
	if r.ev.count(bus.TypeRunStatus, "epic.pushed") != 1 || r.ev.count(bus.TypeRunFailure, "stalled") != 0 {
		t.Fatal("the push was not published once, or the run stalled")
	}
}

// A push that fails every try ends the run with the epic's groups blocked and a stalled fact naming the epic; it is never
// published as pushed.
func TestPushThatKeepsFailingEndsTheRunBlocked(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.commits.errFor = retryable

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	advanceUntilRunEnds(t, r)

	if n := len(r.commits.pushes()); n != PushTries {
		t.Fatalf("%d push tries, want %d", n, PushTries)
	}
	if r.ev.count(bus.TypeRunStatus, "epic.pushed") != 0 || r.ev.count(bus.TypeRunFailure, "stalled") != 1 {
		t.Fatal("a failed push was published as pushed, or no stalled fact named the epic")
	}
	if r.errs.Load() == 0 {
		t.Fatal("the failure was not reported to the Error Handler")
	}
}

// A push error a repeat cannot fix is not retried.
func TestUnretryablePushFailureIsNotRetried(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.commits.errFor = func(int) error { return errors.New("rejected") }

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

	if n := len(r.commits.pushes()); n != 1 {
		t.Fatalf("%d push tries, want 1", n)
	}
}

// A cleanup error after the push is reported on its own and the push is still published.
func TestEpicCleanupErrorStillPublishesThePush(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.commits.result = commit.EpicResult{
		Branch: "komodo/ep-1", Pushed: true, Last: true, CleanupError: "fetch failed",
		PRURL: "https://github.com/acme/shop/pull/41",
	}

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

	pushed, opened := r.ev.count(bus.TypeRunStatus, "epic.pushed"), r.ev.count(bus.TypeRunStatus, "pr.opened")
	if pushed != 1 || opened != 1 || r.errs.Load() != 1 {
		t.Fatalf("epic.pushed %d, pr.opened %d, errors %d", r.ev.count(bus.TypeRunStatus, "epic.pushed"),
			r.ev.count(bus.TypeRunStatus, "pr.opened"), r.errs.Load())
	}
}
