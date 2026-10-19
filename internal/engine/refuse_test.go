package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// spentGroup plants a group that waits for an override until the given time with no retries left.
func spentGroup(t *testing.T, until time.Time) *rig {
	t.Helper()
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle, withClock)
	r.savedGroup("tg-1", StageBlocked, func(s *State) {
		s.Prior, s.Counters.RetriesLeft = StageTestReview, 0
		s.Block = state.BlockState{Reason: state.ReasonRetries, At: t0, Until: until, Counter: "test_review"}
	})
	return r
}

// wantRefused checks an error is the coded refusal and its sentence holds the text.
func wantRefused(t *testing.T, err error, code, text string) {
	t.Helper()
	var ce *codeError
	if !errors.As(err, &ce) || ce.code != code || !strings.Contains(ce.detail, text) {
		t.Fatalf("reply %v, want %s naming %q", err, code, text)
	}
}

func TestRunOnAGroupWaitingForAnOverrideIsRefusedNotAnswered(t *testing.T) {
	r := spentGroup(t, t0.Add(30*time.Minute))
	wantRefused(t, r.startRun("run-2", "tg-1"), CodeRunRefused, "run --override")
	if st := r.store.get("g-tg-1"); st.Stage != StageBlocked || st.Counters.RetriesLeft != 0 {
		t.Fatalf("group %s with %d retries left", st.Stage, st.Counters.RetriesLeft)
	}

	req := &bus.RunContinue{Target: "tg-1", Request: "req-cont"}
	ch := r.m.Expect("req-cont")
	if err := r.bus.Publish(Event{Type: bus.TypeRunContinue, Run: "run-2", Data: req}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-ch:
		wantRefused(t, err, CodeRunRefused, "run --override")
	case <-time.After(5 * time.Second):
		t.Fatal("the continue was not settled")
	}
}

func TestOverrideInsideTheWindowIsAppliedBeforeItReplies(t *testing.T) {
	r := spentGroup(t, t0.Add(30*time.Minute))
	_ = r.startRun("run-2", "tg-1")
	if err := r.m.Override(context.Background(), "tg-1", "ov-1", 45); err != nil {
		t.Fatal(err)
	}
	st := r.store.get("g-tg-1")
	c := st.Counters
	if st.Stage != StageBuild || c.RetriesLeft != 1 || c.Overrides != 1 || c.FinalWallMin != 45 {
		t.Fatalf("group %s, counters %+v", st.Stage, st.Counters)
	}
}

func TestJudgeSessionsThatEndDoneNeverSpendARetry(t *testing.T) {
	for _, agent := range []string{"tester", "reviewer"} {
		end := in(InSessionEnd, SessionEndData{SessionEnd: bus.SessionEnd{
			GroupFacts: bus.GroupFacts{Reason: "done"},
			Agent:      agent,
		}})
		r := apply(t, failedRound(), end)
		if r.State.Counters.RetriesLeft != 2 || slices.Contains(subtypes(r), "retry.spent") || len(r.Orders) != 0 {
			t.Errorf("%s: left %d emits %v orders %v", agent, r.State.Counters.RetriesLeft, subtypes(r), kinds(r))
		}
	}
	for _, reason := range []string{"no_result", "schema_fail", "timeout", "idle"} {
		end := in(InSessionEnd, SessionEndData{SessionEnd: bus.SessionEnd{
			GroupFacts: bus.GroupFacts{Reason: reason},
			Agent:      "tester",
		}})
		if r := apply(t, failedRound(), end); r.State.Counters.RetriesLeft != 1 {
			t.Errorf("%s spent %d, want 1", reason, 2-r.State.Counters.RetriesLeft)
		}
	}
}

func TestOverrideAfterTheWindowIsRefusedBeforeItIsQueued(t *testing.T) {
	r := spentGroup(t, t0)
	_ = r.startRun("run-2", "tg-1")
	wantRefused(t, r.m.Override(context.Background(), "tg-1", "ov-1", 0), CodeRunRefused, "window has passed")
	if st := r.store.get("g-tg-1"); st.Stage != StageBlocked || st.Counters.Overrides != 0 {
		t.Fatalf("group %s, %d overrides", st.Stage, st.Counters.Overrides)
	}
}

func TestOverrideThatLosesTheRaceWithTheLapseIsRefusedWhenTheGroupDecidesIt(t *testing.T) {
	r := spentGroup(t, t0.Add(30*time.Minute))
	_ = r.startRun("run-2", "tg-1")
	g := r.m.byTarget("tg-1")[0]
	late := Input{Kind: InOverride, Request: "ov-late", Time: t0.Add(time.Hour)}
	err := r.m.awaitContinues(context.Background(), []pendingContinue{r.m.queueInput(g, late)})
	wantRefused(t, err, CodeRunRefused, "window has passed")
	if st := r.store.get("g-tg-1"); st.Stage != StageBlocked || st.Counters.RetriesLeft != 0 {
		t.Fatalf("group %s with %d retries left", st.Stage, st.Counters.RetriesLeft)
	}
}

func TestOverrideOnAGroupThatFailedInTheMeantimeIsToldItFailed(t *testing.T) {
	r := spentGroup(t, t0.Add(30*time.Minute))
	_ = r.startRun("run-2", "tg-1")
	g := r.m.byTarget("tg-1")[0]
	r.clock.Advance(time.Hour)
	until(t, "the group failed", func() bool {
		st := r.store.get("g-tg-1")
		return st.Stage == StageDone && st.Outcome == "failed"
	})
	<-g.done

	p := r.m.queueInput(g, Input{Kind: InOverride, Request: "ov-2"})
	err := r.m.awaitContinues(context.Background(), []pendingContinue{p})
	wantRefused(t, err, CodeGroupFailed, "failed for good")
}
