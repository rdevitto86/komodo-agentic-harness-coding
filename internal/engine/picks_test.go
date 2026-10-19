package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// at returns a progress-free input of a harmless kind at an offset from t0.
func at(min int) Input {
	i := in(InFact, FactData{Subtype: "tool.denied"})
	i.Time = t0.Add(time.Duration(min) * time.Minute)
	return i
}

// retrySpentReasons lists the reasons of the retry.spent events of a result.
func retrySpentReasons(r Result) []string {
	var out []string
	for _, e := range r.Emit {
		if p, ok := e.Data.(*bus.RetrySpent); ok {
			out = append(out, p.Reason)
		}
	}
	return out
}

func TestATryGetsOneExtensionOfSixtyMinutesThenFailsAsARetry(t *testing.T) {
	s := claimed()
	s.LastEventAt = t0

	r := apply(t, s, at(60))
	c := r.State.Counters
	if c.WallUsedS != 3600 || c.WallExtTry != 1 || r.State.Stage != StageBuild || c.RetriesLeft != 2 {
		t.Fatalf("at the limit: %+v stage %q", c, r.State.Stage)
	}

	r = apply(t, r.State, at(119))
	if r.State.Stage != StageBuild || r.State.Counters.RetriesLeft != 2 {
		t.Fatalf("inside the extension: stage %q left %d", r.State.Stage, r.State.Counters.RetriesLeft)
	}

	r = apply(t, r.State, at(120))
	c = r.State.Counters
	if r.State.Stage != StageBuild || c.RetriesLeft != 1 || c.Tries != 2 || c.WallBaseS != 7200 {
		t.Fatalf("past the extension: stage %q counters %+v", r.State.Stage, c)
	}
	if got := retrySpentReasons(r); !slices.Equal(got, []string{"wall"}) {
		t.Fatalf("retry.spent reasons %v", got)
	}
	if !slices.Contains(kinds(r), OrderCancel) || !slices.Contains(kinds(r), OrderEnterStage) {
		t.Fatalf("the failed try was not cancelled and restarted: %v", kinds(r))
	}
	if r.State.Tasks[0].State != "queued" {
		t.Fatalf("the task of the cancelled builder stays %q", r.State.Tasks[0].State)
	}
}

func TestTheNextTryStartsWithFreshTimeAndItsOwnExtension(t *testing.T) {
	s := claimed()
	s.LastEventAt = t0
	r := apply(t, apply(t, s, at(60)).State, at(120))

	next := r.State
	next.Tasks[0].State, next.Tasks[0].Session = "claimed", "s2"
	r = apply(t, next, at(150))
	if c := r.State.Counters; c.Tries != 2 || c.WallExtTry != 1 || r.State.Stage != StageBuild || c.RetriesLeft != 1 {
		t.Fatalf("30 minutes into try 2: %+v", c)
	}

	r = apply(t, r.State, at(180))
	if c := r.State.Counters; c.WallExtTry != 2 || c.WallBaseS != 7200 || c.RetriesLeft != 1 {
		t.Fatalf("60 minutes into try 2 must grant its own extension: %+v", c)
	}
}

func TestWallFailureWithNoRetriesLeftStartsTheOverrideWait(t *testing.T) {
	s := claimed()
	s.LastEventAt = t0
	s.Counters.RetriesLeft = 0

	r := apply(t, apply(t, s, at(60)).State, at(120))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonRetries || r.State.Block.Counter != "wall" ||
		r.State.Block.Until.IsZero() {
		t.Fatalf("stage %q block %+v", r.State.Stage, r.State.Block)
	}
}

func TestTheExtensionIsSixtyMinutesWhateverTheGroupLimitIs(t *testing.T) {
	l := fixtureLimits()
	l.GroupWallMin = 30
	req := in(InTimerExtend, map[string]any{"session": "s1", "used": 1, "limit": int64(30 * time.Minute)})
	req.Request = "m1"

	r := Next(claimed(), req, l)
	if len(r.Orders) != 1 || !strings.Contains(string(r.Orders[0].Data), "3600000000000") {
		t.Fatalf("extension order %+v", r.Orders)
	}
}

func TestAnOverrideSendsTheGroupBackToTheBuilderWithTheLastFindings(t *testing.T) {
	s := failedRound()
	s.Counters.RetriesLeft = 0
	fail := in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "t1", Findings: "FINDING: a.go:3 nil dereference"})
	blocked := apply(t, s, fail).State
	if blocked.Stage != StageBlocked || blocked.Block.Reason != state.ReasonRetries {
		t.Fatalf("stage %q block %+v", blocked.Stage, blocked.Block)
	}

	ov := in(InOverride, nil)
	ov.Time = blocked.Block.Until.Add(-time.Minute)

	r := apply(t, blocked, ov)
	if r.State.Stage != StageBuild || r.State.TestReview.Tester.Session != "" || r.State.TestReview.Reviewer.Session != "" {
		t.Fatalf("stage %q round %+v", r.State.Stage, r.State.TestReview)
	}

	var found bool

	for _, o := range r.Orders {
		if o.Kind == OrderEnterStage && o.Stage == StageBuild && strings.Contains(string(o.Data), "nil dereference") {
			found = true
		}
		if o.Kind == OrderEnterStage && o.Stage == StageTestReview {
			t.Fatal("the judges were started before the builder changed anything")
		}
	}
	if !found {
		t.Fatalf("the builder brief lacks the last findings: %+v", r.Orders)
	}
}

func TestAnOverrideAfterABuilderSideExhaustionStillCarriesTheLastReturnedFindings(t *testing.T) {
	s := failedRound()
	s.Counters.RetriesLeft = 2
	fail := in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "t1", Findings: "FINDING: a.go:3 nil dereference"})

	back := apply(t, s, fail).State
	if back.Stage != StageBuild || back.Counters.RetriesLeft != 1 {
		t.Fatalf("stage %q retries %d", back.Stage, back.Counters.RetriesLeft)
	}

	back.Tasks[0].State, back.Tasks[0].Session = "claimed", "s2"
	back.LastEventAt = t0

	blocked := apply(t, apply(t, back, at(60)).State, at(120)).State
	if blocked.Stage != StageBlocked || blocked.Block.Counter != "wall" {
		t.Fatalf("stage %q block %+v", blocked.Stage, blocked.Block)
	}

	ov := in(InOverride, nil)
	ov.Time = blocked.Block.Until.Add(-time.Minute)

	for _, o := range apply(t, blocked, ov).Orders {
		if o.Kind == OrderEnterStage && o.Stage == StageBuild && strings.Contains(string(o.Data), "nil dereference") {
			return
		}
	}

	t.Fatal("the builder brief lacks the findings of the last returned round")
}

func TestOtherBlocksResumeWhereTheyStopped(t *testing.T) {
	s := mk(StageBlocked)
	s.Prior, s.Block = StageTestReview, state.BlockState{Reason: state.ReasonFlap}
	if r := apply(t, s, in(InContinue, nil)); r.State.Stage != StageTestReview {
		t.Fatalf("stage %q", r.State.Stage)
	}
}

// crashEnd builds a crash session.end of an agent.
func crashEnd(agent string) Input {
	return in(InSessionEnd, SessionEndData{
		SessionEnd: bus.SessionEnd{GroupFacts: bus.GroupFacts{Reason: "crash"}, Agent: agent, Task: "t1"},
	})
}

func TestASessionThatEndsWithAWorkOutcomeBreaksTheRunOfKomodoFaults(t *testing.T) {
	s := claimed()
	for i, end := range []Input{crashEnd("builder"), builderEnd("timeout"), crashEnd("builder"), crashEnd("builder")} {
		r := apply(t, s, end)
		if r.State.Stage != StageBuild || r.State.Block.Reason == state.ReasonCrash {
			t.Fatalf("end %d blocked the group: stage %q block %+v", i+1, r.State.Stage, r.State.Block)
		}

		s = r.State
		s.Tasks[0].State, s.Tasks[0].Session = "claimed", "s1"
	}
	if got := s.Counters.FaultStreak; got != 2 {
		t.Fatalf("fault streak %d, want 2", got)
	}
}

func TestFourBlockerErrorsInARowBlockTheGroupForAPersonAndSpendNoRetry(t *testing.T) {
	s := claimed()
	for i := 1; i <= 3; i++ {
		r := apply(t, s, crashEnd("builder"))
		if r.State.Stage != StageBuild || r.State.Counters.FaultStreak != i || !slices.Contains(kinds(r), OrderEnterStage) {
			t.Fatalf("fault %d: stage %q streak %d", i, r.State.Stage, r.State.Counters.FaultStreak)
		}

		s = r.State
		s.Tasks[0].State, s.Tasks[0].Session = "claimed", "s1"
	}

	r := apply(t, s, crashEnd("builder"))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonCrash || r.State.Counters.RetriesLeft != 2 {
		t.Fatalf("stage %q block %+v left %d", r.State.Stage, r.State.Block, r.State.Counters.RetriesLeft)
	}
	if slices.Contains(subtypes(r), "group.failed") || slices.Contains(kinds(r), OrderEnterStage) {
		t.Fatalf("the capped group failed or restarted: %v %v", subtypes(r), kinds(r))
	}

	cont := apply(t, r.State, in(InContinue, nil))
	if cont.State.Stage != StageBuild || cont.State.Counters.FaultStreak != 0 {
		t.Fatalf("after the fix: stage %q streak %d", cont.State.Stage, cont.State.Counters.FaultStreak)
	}
}

func TestAnyAgentsCrashCountsAndAResultResetsTheStreak(t *testing.T) {
	s := failedRound()
	s.Counters.FaultStreak = 3
	r := apply(t, s, crashEnd("reviewer"))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonCrash {
		t.Fatalf("a judge's fourth crash: stage %q block %+v", r.State.Stage, r.State.Block)
	}

	s.Counters.FaultStreak = 3
	r = apply(t, s, verifyEnd("tester", true))
	if r.State.Counters.FaultStreak != 0 {
		t.Fatalf("a session that ended with a result left streak %d", r.State.Counters.FaultStreak)
	}
}

func TestACrashFromTheEngineRestartingIsNoFault(t *testing.T) {
	s := claimed()
	end := sessionCrash("builder", "s1", "t1", StageBuild)
	r := apply(t, s, in(InSessionEnd, end))
	if r.State.Counters.FaultStreak != 0 || r.Blocker != "" {
		t.Fatalf("streak %d panic %q", r.State.Counters.FaultStreak, r.Blocker)
	}
}

func TestAGuardThatFailsToLoadBlocksUnderSetupSpendsNoRetryAndCountsAsAFault(t *testing.T) {
	s := claimed()
	detail := `{"error":"guard row G4 is missing","file":"policy.json"}`
	r := apply(t, s, in(InFault, FaultData{Reason: "setup", Detail: detail}))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonSetup || r.State.Counters.RetriesLeft != 2 {
		t.Fatalf("stage %q block %+v left %d", r.State.Stage, r.State.Block, r.State.Counters.RetriesLeft)
	}
	if r.State.Counters.FaultStreak != 1 || r.Blocker != state.ReasonSetup {
		t.Fatalf("streak %d panic %q", r.State.Counters.FaultStreak, r.Blocker)
	}
	for _, e := range r.Emit {
		if b, ok := e.Data.(*bus.GroupBlocked); ok && !strings.Contains(b.Detail, "policy.json") {
			t.Fatalf("the event does not name the rules file: %q", b.Detail)
		}
	}
}

func TestAToolErrorInsideASessionIsNoFault(t *testing.T) {
	s := claimed()
	for _, i := range []Input{
		in(InFact, FactData{Subtype: "tool.error", Data: data(map[string]string{"error": "bad regex: missing closing )"})}),
		in(InFact, FactData{Subtype: "tool.denied"}),
		in(InGateFail, bus.TaskDone{Task: "t1"}),
	} {
		r := Next(s, i, fixtureLimits())
		if r.Blocker != "" || r.State.Counters.FaultStreak != 0 || r.State.Counters.Crashes != 0 {
			t.Errorf("%s counted: panic %q streak %d", i.Kind, r.Blocker, r.State.Counters.FaultStreak)
		}
	}
}
