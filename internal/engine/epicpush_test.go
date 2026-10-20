package engine

import (
	"errors"
	"slices"
	"testing"

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

// A failed push is reported to the Error Handler and does not keep the run from ending; nothing is retried in the run.
func TestFailedEpicPushIsReportedAndTheRunStillEnds(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	r.commits.err = errors.New("network down")

	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

	if n := len(r.commits.pushes()); n != 1 {
		t.Fatalf("%d pushes, want 1", n)
	}
	if r.ev.count(bus.TypeRunStatus, "epic.pushed") != 0 {
		t.Fatal("a failed push was published as pushed")
	}
}
