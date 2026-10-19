package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// emptyBudget returns a Test and Review group that has just spent its last retry and then failed again.
func emptyBudget(t *testing.T) State {
	t.Helper()
	s := mk(StageTestReview)
	s.Counters.RetriesLeft = 0
	return apply(t, s, in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "t1"})).State
}

func TestReachingZeroEntersTheOverrideWaitAndRunNeverRaisesTheBudget(t *testing.T) {
	s := emptyBudget(t)
	if s.Stage != StageBlocked || s.Block.Reason != state.ReasonRetries || s.Block.Until.IsZero() {
		t.Fatalf("stage %q block %+v", s.Stage, s.Block)
	}

	r := Next(s, in(InContinue, nil), fixtureLimits())
	if !r.Refused || r.State.Counters.RetriesLeft != 0 || r.State.Stage != StageBlocked {
		t.Fatalf("run on a group waiting for an override: refused %v state %q left %d",
			r.Refused, r.State.Stage, r.State.Counters.RetriesLeft)
	}

	req := in(InContinue, nil)
	req.Request = "req-run"
	if r := Next(s, req, fixtureLimits()); r.Emit == nil || r.Emit[0].Subtype != "request.refused" {
		t.Fatalf("the refused run was not told why: %+v", r.Emit)
	}
}

func TestOverrideInsideTheWindowGrantsTheFinalTryAndContinues(t *testing.T) {
	s := emptyBudget(t)
	ov := in(InOverride, OverrideData{Minutes: 60})
	ov.Time = s.Block.Until.Add(-time.Minute)

	r := apply(t, s, ov)
	c := r.State.Counters
	if r.State.Stage != StageBuild || c.RetriesLeft != 1 || c.Overrides != 1 || !r.State.Block.Until.IsZero() {
		t.Fatalf("stage %q counters %+v block %+v", r.State.Stage, c, r.State.Block)
	}
	if !slices.Contains(subtypes(r), "override.approved") || !slices.Contains(subtypes(r), "group.resumed") ||
		!slices.Contains(kinds(r), OrderEnterStage) || r.State.Epoch != s.Epoch+1 {
		t.Fatalf("emits %v orders %v epoch %d", subtypes(r), kinds(r), r.State.Epoch)
	}
}

func TestOverrideAfterTheWindowOrOnAnotherBlockIsRefused(t *testing.T) {
	s := emptyBudget(t)
	late := in(InOverride, nil)
	late.Time = s.Block.Until.Add(time.Second)
	if r := Next(s, late, fixtureLimits()); !r.Refused || r.State.Counters.Overrides != 0 {
		t.Fatalf("a late override was applied: %+v", r.State.Counters)
	}

	other := mk(StageBlocked)
	other.Prior, other.Block = StageBuild, state.BlockState{Reason: state.ReasonFlap}
	if r := Next(other, in(InOverride, nil), fixtureLimits()); !r.Refused {
		t.Fatal("an override answered a block that waits for nothing")
	}
	if r := Next(mk(StageBuild), in(InOverride, nil), fixtureLimits()); !r.Refused {
		t.Fatal("an override answered a running group")
	}
}

func TestTheWindowLapsingFailsTheGroupAndHandsItToCleanup(t *testing.T) {
	s := emptyBudget(t)
	due := in(InTimerDue, TimerDue{Reason: "override_lapse"})
	due.Time = s.Block.Until

	r := apply(t, s, due)
	if r.State.Stage != StageDone || r.State.Outcome != "failed" || r.State.Counters.Overrides != 0 {
		t.Fatalf("stage %q outcome %q", r.State.Stage, r.State.Outcome)
	}
	for _, want := range []string{"override.lapsed", "group.failed", "group.done"} {
		if !slices.Contains(subtypes(r), want) {
			t.Errorf("missing event %s in %v", want, subtypes(r))
		}
	}
	if !slices.Contains(kinds(r), OrderRelease) || slices.Contains(kinds(r), OrderDiscard) {
		t.Fatalf("a failed group releases its lease and is left to the Cleanup Handler: %v", kinds(r))
	}
	for _, e := range r.Emit {
		if f, ok := e.Data.(*bus.GroupFailed); ok && (f.Last != "test_review" || f.Prior != "test_review") {
			t.Errorf("group.failed body %+v", f)
		}
	}

	again := Next(r.State, in(InContinue, nil), fixtureLimits())
	if !again.Refused || again.State.Stage != StageDone || again.State.Outcome != "failed" {
		t.Fatalf("a failed group was continued: %q", again.State.Stage)
	}
	early := Next(s, in(InTimerDue, TimerDue{Reason: "override_lapse"}), fixtureLimits())
	if early.State.Stage == StageDone {
		t.Fatal("the window lapsed before it ended")
	}
}

func TestWhatKomodoBreaksCostsNoRetryAndIsRecordedApart(t *testing.T) {
	r := apply(t, mk(StageBuild), in(InLoopRestart, nil))
	if r.State.Counters.RetriesLeft != 2 || !slices.Contains(subtypes(r), "retry.waived") {
		t.Fatalf("a Task Manager restart: left %d emits %v", r.State.Counters.RetriesLeft, subtypes(r))
	}

	s := failedRound()
	for _, reason := range []string{"crash", "provider"} {
		end := in(InSessionEnd, SessionEndData{
			SessionEnd: bus.SessionEnd{GroupFacts: bus.GroupFacts{Reason: reason}, Agent: "reviewer"},
		})
		r := apply(t, s, end)
		if r.State.Counters.RetriesLeft != 2 || !slices.Contains(subtypes(r), "retry.waived") {
			t.Errorf("reviewer %s: left %d emits %v", reason, r.State.Counters.RetriesLeft, subtypes(r))
		}
	}
}

func TestAFaultFromTheTaskManagerThatNamesRetriesEmptiesTheBudget(t *testing.T) {
	r := apply(t, mk(StageBuild), in(InFault, FaultData{Reason: "retries", Detail: "timeout"}))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonRetries || r.State.Counters.RetriesLeft != 0 {
		t.Fatalf("stage %q block %+v left %d", r.State.Stage, r.State.Block, r.State.Counters.RetriesLeft)
	}
}

func TestAMigratedGroupBlockedOnRetriesWithABudgetContinues(t *testing.T) {
	s := mk(StageBlocked)
	s.Prior, s.Block = StageBuild, state.BlockState{Reason: state.ReasonRetries}
	r := apply(t, s, in(InContinue, nil))
	if r.State.Stage != StageBuild || r.State.Counters.RetriesLeft != 2 {
		t.Fatalf("stage %q left %d", r.State.Stage, r.State.Counters.RetriesLeft)
	}
}

func TestASecondExhaustionAfterAnApprovedOverrideFailsAtOnceWithNoPrompt(t *testing.T) {
	s := emptyBudget(t)
	ov := in(InOverride, OverrideData{Minutes: 60})
	ov.Time = s.Block.Until.Add(-time.Minute)
	granted := apply(t, s, ov).State

	granted.Counters.RetriesLeft = 0
	granted.Tasks[0].State, granted.Tasks[0].Session = "claimed", "s1"
	r := apply(t, granted, builderEnd("idle"))
	if r.State.Stage != StageDone || r.State.Outcome != "failed" || r.State.Counters.Overrides != 1 {
		t.Fatalf("stage %q outcome %q overrides %d", r.State.Stage, r.State.Outcome, r.State.Counters.Overrides)
	}
	if slices.Contains(subtypes(r), "group.blocked") || !slices.Contains(subtypes(r), "group.failed") ||
		!r.State.Block.Until.IsZero() {
		t.Fatalf("a second override was offered: emits %v block %+v", subtypes(r), r.State.Block)
	}
}
