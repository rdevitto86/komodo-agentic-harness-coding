package engine

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// isBlocker reports whether the error a result stood for is a blocker.
func isBlocker(r Result) bool { return state.Classify(r.Err) == state.SeverityBlocker }

func TestAWorkOutcomeNeverCountsAsABlocker(t *testing.T) {
	for _, reason := range []string{"check_fail", "timeout", "idle", "no_result", "schema_fail", "refusals"} {
		r := apply(t, claimed(), builderEnd(reason))
		if isBlocker(r) || r.State.Counters.FaultStreak != 0 {
			t.Errorf("builder end %s: blocker %q streak %d", reason, r.Err, r.State.Counters.FaultStreak)
		}
	}

	for _, agent := range []string{"tester", "reviewer"} {
		r := apply(t, failedRound(), verifyEnd(agent, false, "TestAdd"))
		if isBlocker(r) || r.State.Counters.FaultStreak != 0 {
			t.Errorf("a failed %s round: blocker %q streak %d", agent, r.Err, r.State.Counters.FaultStreak)
		}
	}

	r := apply(t, mk(StageTestReview), in(InResultFail, ResultFail{Items: []string{"lint: unused"}}))
	if isBlocker(r) || r.State.Counters.FaultStreak != 0 {
		t.Errorf("a failed lint check: blocker %q streak %d", r.Err, r.State.Counters.FaultStreak)
	}
}

func TestAStepAfterTheWorkBreakingIsNoBlocker(t *testing.T) {
	for _, reason := range []string{"publish", "cleanup", "commit"} {
		stage := StageCommit
		if reason == "cleanup" {
			stage = StageCleanup
		}

		r := apply(t, mk(stage), in(InFault, FaultData{Reason: reason, Detail: "x"}))
		if isBlocker(r) || r.State.Counters.FaultStreak != 0 {
			t.Errorf("fault %s: blocker %q streak %d", reason, r.Err, r.State.Counters.FaultStreak)
		}
	}
}

func TestASessionCrashCountsAsABlocker(t *testing.T) {
	r := apply(t, claimed(), crashEnd("builder"))
	if r.Err != state.ReasonCrash || r.State.Counters.FaultStreak != 1 {
		t.Fatalf("blocker %q streak %d", r.Err, r.State.Counters.FaultStreak)
	}
}
