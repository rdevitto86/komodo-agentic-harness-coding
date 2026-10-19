package engine

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestEveryStageWithEveryInput(t *testing.T) {
	stages := []Stage{
		NoStage,
		StageIntake,
		StageBuild,
		StageTestReview,
		StageCommit,
		StageCleanup,
		StagePaused,
		StageBlocked,
		StageDone,
	}
	all := []InputKind{
		InGroupStart,
		InLeaseGranted,
		InReady,
		InStageDone,
		InResultFail,
		InSessionStart,
		InSessionEnd,
		InTaskDone,
		InGateFail,
		InScopeAsk,
		InScopeAnswer,
		InTimerExtend,
		InTimerDue,
		InContinue,
		InLeaseLost,
		InFault,
		InGroupClosed,
		InGroupCrash,
		InLimitPause,
		InAbandon,
		InProgress,
		InFact,
	}
	for _, st := range stages {
		for _, k := range all {
			s := mk(st)
			if st == StagePaused || st == StageBlocked {
				s.Prior = StageBuild
			}
			if st == NoStage {
				s = State{}
			}
			r := Next(s, Input{Kind: k, Group: "g1", Time: t0.Add(time.Minute)}, fixtureLimits())
			if r.Refused && !stateEqual(r.State, s) {
				t.Errorf("%s in %q: refused input changed the state", k, st)
			}
		}
	}
}

func TestHappyPathStageMoves(t *testing.T) {
	s := State{}
	r := apply(t, s, in(InGroupStart, nil))
	if r.State.Stage != StageIntake || r.State.Epoch != 1 || !slices.Contains(kinds(r), OrderGrantLease) {
		t.Fatalf("row 1: stage %q epoch %d orders %v", r.State.Stage, r.State.Epoch, kinds(r))
	}
	s = r.State
	r = apply(t, s, in(InLeaseGranted, LeaseGrant{Fence: 9, Branch: "komodo/ep-1-tg-1", Worktree: "/w"}))
	if r.State.Fence != 9 || !slices.Contains(kinds(r), OrderStartGroup) {
		t.Fatalf("row 2: fence %d orders %v", r.State.Fence, kinds(r))
	}
	s = r.State
	s.Tasks = []state.TaskState{{ID: "t1", State: "queued"}}
	r = apply(t, s, in(InReady, nil))
	if r.State.Stage != StageBuild || r.State.Epoch != 2 || !slices.Contains(subtypes(r), "stage.enter") {
		t.Fatalf("row 3: stage %q epoch %d emits %v", r.State.Stage, r.State.Epoch, subtypes(r))
	}
	s = r.State
	if r2 := Next(s, in(InStageDone, StageDoneData{Stage: StageBuild}), fixtureLimits()); !r2.Refused {
		t.Fatalf("row 4 applied while a task was not done")
	}
	s.Tasks[0].State = "done"
	r = apply(t, s, in(InStageDone, StageDoneData{Stage: StageBuild}))
	if r.State.Stage != StageTestReview || r.State.Epoch != 3 {
		t.Fatalf("row 4: stage %q epoch %d", r.State.Stage, r.State.Epoch)
	}
	r = apply(t, r.State, in(InStageDone, StageDoneData{Stage: StageTestReview}))
	if r.State.Stage != StageCommit || r.State.Epoch != 4 {
		t.Fatalf("row 5: stage %q epoch %d", r.State.Stage, r.State.Epoch)
	}
	r = apply(t, r.State, in(InStageDone, StageDoneData{Stage: StageCommit}))
	if r.State.Stage != StageCleanup {
		t.Fatalf("row 7: stage %q", r.State.Stage)
	}
	r = apply(t, r.State, in(InGroupClosed, nil))
	if r.State.Stage != StageDone || r.State.Outcome != "complete" || !slices.Contains(kinds(r), OrderRelease) ||
		!slices.Contains(subtypes(r), "group.done") {
		t.Fatalf("row 8: stage %q outcome %q orders %v emits %v", r.State.Stage, r.State.Outcome, kinds(r), subtypes(r))
	}
	for _, o := range r.Orders {
		if o.Key == "" {
			t.Fatalf("order %s has no key", o.Kind)
		}
	}
	for _, o := range r.Orders {
		if !slices.ContainsFunc(r.State.ActionsOpen, func(a state.OpenAction) bool { return a.ID == o.Key }) {
			t.Fatalf("order %s is not in the open actions", o.Key)
		}
	}
}

func TestStaleInputAndDuplicateRequest(t *testing.T) {
	s := mk(StageTestReview)
	stale := in(InResultFail, ResultFail{Items: []string{"x"}, Tree: "t"})
	stale.Epoch = 2
	if r := Next(s, stale, fixtureLimits()); !r.Stale || !stateEqual(r.State, s) {
		t.Fatalf("stale input: stale=%v", r.Stale)
	}
	c := in(InContinue, nil)
	c.Request = "req-1"
	s = mk(StagePaused)
	s.Prior = StageBuild
	s.Pause = state.PauseState{Cause: "provider", Since: t0}
	r := apply(t, s, c)
	if !slices.Contains(r.State.Requests, "req-1") {
		t.Fatalf("request not recorded: %v", r.State.Requests)
	}
	if r2 := Next(r.State, c, fixtureLimits()); !r2.Dup {
		t.Fatalf("a replayed request was applied twice")
	}
	big := mk(StageBuild)
	for i := range 70 {
		big.Requests = append(big.Requests, "r"+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	i2 := in(InFact, FactData{Subtype: "tool.denied"})
	i2.Request = "new"
	if r := Next(big, i2, fixtureLimits()); len(r.State.Requests) > 64 {
		t.Fatalf("requests grew to %d", len(r.State.Requests))
	}
}

func TestTesterFailsThenReviewerApprovesReturnsOnce(t *testing.T) {
	for _, order := range [][2]string{{"tester", "reviewer"}, {"reviewer", "tester"}} {
		s := failedRound()
		r := apply(t, s, verifyEnd(order[0], order[0] != "tester", "TestA"))
		if r.State.Stage != StageTestReview {
			t.Fatalf("%v: moved at the first end to %q", order, r.State.Stage)
		}
		r = apply(t, r.State, verifyEnd(order[1], order[1] != "tester", "TestA"))
		if r.State.Stage != StageBuild || r.State.Counters.Returns != 1 || r.State.FailureHash == "" {
			t.Fatalf("%v: stage %q returns %d hash %q", order, r.State.Stage, r.State.Counters.Returns, r.State.FailureHash)
		}
		if got := kinds(r); !slices.Contains(got, OrderEnterStage) {
			t.Fatalf("%v: orders %v", order, got)
		}
	}
}

func TestReturnsBlockAtTheLimitAndRegressionNamesItself(t *testing.T) {
	s := mk(StageTestReview)
	s.Counters.Returns = 2
	r := apply(t, s, in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "t1"}))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonReturns || r.State.Prior != StageTestReview {
		t.Fatalf("stage %q reason %q prior %q", r.State.Stage, r.State.Block.Reason, r.State.Prior)
	}
	r = apply(t, s, in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "t1", Regression: true}))
	if r.State.Block.Reason != state.ReasonRegression {
		t.Fatalf("reason %q", r.State.Block.Reason)
	}
	if r.State.Counters.Blocks["regression"] != 1 || !slices.Contains(subtypes(r), "group.blocked") ||
		!slices.Contains(kinds(r), OrderCancel) {
		t.Fatalf("blocks %v emits %v orders %v", r.State.Counters.Blocks, subtypes(r), kinds(r))
	}
}

func TestFlapOnTheSameTreeBlocks(t *testing.T) {
	s := mk(StageTestReview)
	r := apply(t, s, in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "tree-1"}))
	if r.State.Stage != StageBuild || r.State.ReturnTree != "tree-1" {
		t.Fatalf("first return: %q tree %q", r.State.Stage, r.State.ReturnTree)
	}
	back := r.State
	back.Stage, back.Epoch = StageTestReview, back.Epoch+1
	r = apply(t, back, in(InResultFail, ResultFail{Items: []string{"b"}, Tree: "tree-1"}))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonFlap {
		t.Fatalf("same tree: stage %q reason %q", r.State.Stage, r.State.Block.Reason)
	}
	r = apply(t, back, in(InResultFail, ResultFail{Items: []string{"a"}, Tree: "tree-2"}))
	if r.State.Block.Reason != state.ReasonFlap {
		t.Fatalf("same fingerprint: reason %q", r.State.Block.Reason)
	}
}

func TestMechanicalCheckFailureReturnsToBuildWithTheOutput(t *testing.T) {
	r := apply(
		t,
		mk(StageTestReview),
		in(InResultFail, ResultFail{Items: []string{"vet"}, Tree: "t", Findings: "main.go:3: vet: unreachable code"}),
	)
	if r.State.Stage != StageBuild {
		t.Fatalf("stage %q", r.State.Stage)
	}
	var o *Order
	for i := range r.Orders {
		if r.Orders[i].Kind == OrderEnterStage {
			o = &r.Orders[i]
		}
	}
	if o == nil || !strings.Contains(string(o.Data), "unreachable code") || o.Stage != StageBuild {
		t.Fatalf("enter_stage order = %+v", o)
	}
	for _, k := range kinds(r) {
		if k == OrderStartGroup {
			t.Fatalf("a model session was ordered: %v", kinds(r))
		}
	}
}

func TestBuilderTimeoutBlocksRetriesPastTheLimit(t *testing.T) {
	s := claimed()
	s.Counters.Retries = 1
	s.Counters.Attempts = 1
	r := apply(t, s, builderEnd("timeout"))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonRetries {
		t.Fatalf("stage %q reason %q", r.State.Stage, r.State.Block.Reason)
	}
	s.Counters.Retries = 0
	r = apply(t, s, builderEnd("timeout"))
	if r.State.Stage != StageBuild || r.State.Counters.Retries != 1 || !slices.Contains(kinds(r), OrderEnterStage) {
		t.Fatalf("within retries: stage %q retries %d orders %v", r.State.Stage, r.State.Counters.Retries, kinds(r))
	}
	if r.State.Counters.TokensUsed != 100 || r.State.Tasks[0].Session != "" {
		t.Fatalf("tokens %d session %q", r.State.Counters.TokensUsed, r.State.Tasks[0].Session)
	}
}

func TestAttemptsCountFreshSessionsOnly(t *testing.T) {
	s := mk(StageBuild)
	start := func(resumed, restart bool) Input {
		return in(InSessionStart, SessionStartData{
			SessionStart: bus.SessionStart{
				Agent:      "builder",
				Task:       "t1",
				GroupFacts: bus.GroupFacts{Session: "s1"},
				Resumed:    resumed,
			},
			Restart: restart,
		})
	}
	r := apply(t, s, start(false, false))
	if r.State.Counters.Attempts != 1 || r.State.Tasks[0].State != "claimed" || r.State.Tasks[0].Session != "s1" {
		t.Fatalf("fresh start: attempts %d task %+v", r.State.Counters.Attempts, r.State.Tasks[0])
	}
	if r := apply(t, s, start(false, true)); r.State.Counters.Attempts != 0 {
		t.Fatalf("crash restart charged an attempt")
	}
	s2 := claimed()
	s2.Counters.Attempts = 2
	r = apply(t, s2, builderEnd("idle"))
	if r.State.Block.Reason != state.ReasonAttempts {
		t.Fatalf("reason %q", r.State.Block.Reason)
	}
}

func TestWallTimeExtendsThenBlocks(t *testing.T) {
	s := claimed()
	s.LastEventAt = t0
	i := in(InFact, FactData{Subtype: "tool.denied"})
	i.Time = t0.Add(60 * time.Minute)
	r := apply(t, s, i)
	if r.State.Counters.WallUsedS != 3600 || !r.State.Counters.WallExtended || r.State.Stage != StageBuild {
		t.Fatalf(
			"at the limit: used %d extended %v stage %q",
			r.State.Counters.WallUsedS,
			r.State.Counters.WallExtended,
			r.State.Stage,
		)
	}
	i.Time = t0.Add(120 * time.Minute)
	r = apply(t, r.State, i)
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonWall {
		t.Fatalf("at twice: stage %q reason %q", r.State.Stage, r.State.Block.Reason)
	}
}

func TestIdleTimeAddsNoWallTime(t *testing.T) {
	long := t0.Add(10 * time.Hour)
	cases := map[string]State{
		"no live session": mk(StageBuild),
		"paused": func() State {
			s := claimed()
			s.Stage, s.Prior = StagePaused, StageBuild
			s.Pause = state.PauseState{Cause: "provider", Since: t0}
			return s
		}(),
		"blocked":         func() State { s := claimed(); s.Stage, s.Prior = StageBlocked, StageBuild; return s }(),
		"test and review": mk(StageTestReview),
	}
	for name, s := range cases {
		i := in(InFact, FactData{Subtype: "tool.denied"})
		i.Time = long
		r := Next(s, i, fixtureLimits())
		if r.State.Counters.WallUsedS != 0 {
			t.Errorf("%s: wall used %d", name, r.State.Counters.WallUsedS)
		}
	}
	s := claimed()
	i := in(InFact, FactData{Subtype: "tool.denied"})
	i.Time = t0.Add(90 * time.Second)
	if r := apply(t, s, i); r.State.Counters.WallUsedS != 90 {
		t.Fatalf("live builder: wall used %d", r.State.Counters.WallUsedS)
	}
}

func TestContinueResetsExactlyTheNamedCounters(t *testing.T) {
	cases := []struct {
		reason state.Reason
		set    func(*State)
		check  func(State) bool
		prior  Stage
	}{
		{
			state.ReasonReturns,
			func(s *State) { s.Counters.Returns = 2; s.FailureHash = "h"; s.ReturnTree = "t" },
			func(s State) bool { return s.Counters.Returns == 0 && s.FailureHash == "" && s.ReturnTree == "" },
			StageTestReview,
		},
		{
			state.ReasonRegression,
			func(s *State) { s.Counters.Returns = 2; s.FailureHash = "h" },
			func(s State) bool { return s.Counters.Returns == 0 && s.FailureHash == "" },
			StageTestReview,
		},
		{
			state.ReasonFlap,
			func(s *State) { s.Counters.Returns = 1; s.FailureHash = "h"; s.ReturnTree = "t" },
			func(s State) bool { return s.Counters.Returns == 1 && s.FailureHash == "" && s.ReturnTree == "" },
			StageTestReview,
		},
		{
			state.ReasonRetries,
			func(s *State) { s.Counters.Retries = 2; s.Counters.Attempts = 1 },
			func(s State) bool { return s.Counters.Retries == 0 && s.Counters.Attempts == 1 },
			StageBuild,
		},
		{
			state.ReasonAttempts,
			func(s *State) { s.Counters.Attempts = 2; s.Counters.Retries = 1 },
			func(s State) bool { return s.Counters.Attempts == 0 && s.Counters.Retries == 1 },
			StageBuild,
		},
		{
			state.ReasonWall,
			func(s *State) { s.Counters.WallUsedS = 7200; s.Counters.WallExtended = true },
			func(s State) bool { return s.Counters.WallUsedS == 0 && !s.Counters.WallExtended },
			StageBuild,
		},
		{state.ReasonNoProvider, func(s *State) {}, func(s State) bool { return true }, StageBuild},
		{state.ReasonCrash, func(s *State) {}, func(s State) bool { return true }, StageBuild},
	}
	for _, c := range cases {
		s := mk(StageBlocked)
		s.Prior = c.prior
		s.Block = state.BlockState{Reason: c.reason, At: t0}
		s.Counters.Blocks[string(c.reason)] = 3
		c.set(&s)
		r := apply(t, s, in(InContinue, nil))
		if r.State.Stage != c.prior || !c.check(r.State) {
			t.Errorf("%s: stage %q state %+v", c.reason, r.State.Stage, r.State.Counters)
		}
		if r.State.Counters.Blocks[string(c.reason)] != 3 {
			t.Errorf("%s: a continue changed Blocks", c.reason)
		}
		if r.State.Epoch != s.Epoch+1 || !slices.Contains(subtypes(r), "group.resumed") ||
			!slices.Contains(subtypes(r), "stage.enter") {
			t.Errorf("%s: epoch %d emits %v", c.reason, r.State.Epoch, subtypes(r))
		}
		if r.State.Block.Reason != "" || r.State.Prior != "" {
			t.Errorf("%s: block %+v prior %q survive", c.reason, r.State.Block, r.State.Prior)
		}
	}
	// A second block of the same reason follows only when the cause remains.
	s := claimed()
	s.Counters.Retries, s.Counters.Attempts = 0, 1
	if r := apply(t, s, builderEnd("timeout")); r.State.Stage != StageBuild {
		t.Fatalf("after a reset the group blocked again at once: %q", r.State.Stage)
	}
}

func TestContinueOnNeedsScopeWithoutAnAnswerIsRefused(t *testing.T) {
	s := mk(StageBlocked)
	s.Prior = StageBuild
	s.Block = state.BlockState{Reason: state.ReasonNeedsScope}
	c := in(InContinue, nil)
	c.Request = "q1"
	r := Next(s, c, fixtureLimits())
	if !r.Refused || !stateEqual(r.State, s) {
		t.Fatalf("refused=%v", r.Refused)
	}
	if !slices.Contains(subtypes(r), "request.refused") {
		t.Fatalf("emits %v", subtypes(r))
	}
}

func TestLimitPauseWithAndWithoutALiveSession(t *testing.T) {
	until := t0.Add(2 * time.Hour)
	lp := LimitPause{Link: "claude-cli", Cause: "plan_window", Until: until.Add(time.Minute)}
	r := apply(t, mk(StageBuild), in(InLimitPause, lp))
	if r.State.Stage != StagePaused || r.State.Prior != StageBuild || !r.State.Pause.Until.Equal(until.Add(time.Minute)) ||
		r.State.Pause.Cause != "plan_window" {
		t.Fatalf("idle group: stage %q pause %+v", r.State.Stage, r.State.Pause)
	}
	if !slices.Contains(kinds(r), OrderHold) || !slices.Contains(subtypes(r), "group.paused") {
		t.Fatalf("orders %v emits %v", kinds(r), subtypes(r))
	}
	r = apply(t, claimed(), in(InLimitPause, lp))
	if r.State.Stage != StageBuild || !r.State.Pause.After {
		t.Fatalf("live group: stage %q after %v", r.State.Stage, r.State.Pause.After)
	}
	r = apply(t, r.State, builderEnd("done"))
	if r.State.Stage != StagePaused || r.State.Pause.After || !slices.Contains(kinds(r), OrderHold) {
		t.Fatalf("at the session end: stage %q after %v orders %v", r.State.Stage, r.State.Pause.After, kinds(r))
	}
}

func TestProviderPauseAndRefusedResume(t *testing.T) {
	pe := in(InSessionEnd, SessionEndData{
		SessionEnd: bus.SessionEnd{
			GroupFacts: bus.GroupFacts{Reason: "paused"},
			Agent:      "builder",
			Handle:     "h1",
			Link:       "claude-cli",
			ResetAt:    t0.Add(time.Hour),
		},
	})
	r := apply(t, claimed(), pe)
	p := r.State.Pause
	if r.State.Stage != StagePaused || p.Cause != "provider" || !p.Until.Equal(t0.Add(time.Hour+time.Minute)) ||
		p.Since.IsZero() || len(r.State.Resume) != 1 || r.State.Resume[0].Handle != "h1" {
		t.Fatalf("stage %q pause %+v resume %v", r.State.Stage, p, r.State.Resume)
	}
	since := p.Since
	due := in(InTimerDue, TimerDue{Reason: "timer"})
	r = apply(t, r.State, due)
	if r.State.Stage != StageBuild || r.State.Epoch != 3 || r.State.Pause.Since.IsZero() ||
		!slices.Contains(kinds(r), OrderEnterStage) {
		t.Fatalf("resume: stage %q epoch %d orders %v", r.State.Stage, r.State.Epoch, kinds(r))
	}
	if slices.Contains(subtypes(r), "stage.enter") || !slices.Contains(subtypes(r), "group.resumed") {
		t.Fatalf("a pause resume emitted %v", subtypes(r))
	}
	again := pe
	again.Time = t0.Add(10 * time.Minute)
	again.Data = data(SessionEndData{
		SessionEnd: bus.SessionEnd{GroupFacts: bus.GroupFacts{Reason: "paused"}, Agent: "builder", Handle: "h1"},
	})
	s := r.State
	s.Tasks[0].State, s.Tasks[0].Session = "claimed", "s1"
	r = apply(t, s, again)
	if r.State.Pause.Refused != 1 || !r.State.Pause.Since.Equal(since) ||
		r.State.Pause.Until.Sub(again.Time) != 15*time.Minute {
		t.Fatalf("refused resume: %+v", r.State.Pause)
	}
}

func TestPauseTimeoutAppliesToProviderPausesOnly(t *testing.T) {
	late := in(InTimerDue, TimerDue{Reason: "pause_timeout"})
	late.Time = t0.Add(361 * time.Minute)
	for cause, want := range map[string]bool{
		"provider":     true,
		"plan_window":  false,
		"spending":     false,
		"group_tokens": false,
	} {
		s := mk(StagePaused)
		s.Prior = StageBuild
		s.Pause = state.PauseState{Cause: cause, Since: t0, Until: t0.Add(time.Hour)}
		r := Next(s, late, fixtureLimits())
		blocked := r.State.Stage == StageBlocked && r.State.Block.Reason == state.ReasonPauseTimeout
		if blocked != want {
			t.Errorf("%s: blocked=%v want %v", cause, blocked, want)
		}
		if want && (r.State.Prior != StageBuild || !slices.Contains(kinds(r), OrderCancel)) {
			t.Errorf("prior %q orders %v", r.State.Prior, kinds(r))
		}
	}
}

func TestContinueFromCommitGoesToTestAndReview(t *testing.T) {
	s := mk(StageCommit)
	s.ActionsOpen = []state.OpenAction{
		{ID: "k1", Kind: string(OrderEnterStage), Args: data(Order{Kind: OrderEnterStage, Stage: StageCommit})},
	}
	r := apply(t, s, in(InContinue, nil))
	if r.State.Stage != StageTestReview || r.State.Epoch != s.Epoch+1 {
		t.Fatalf("stage %q epoch %d", r.State.Stage, r.State.Epoch)
	}
	for _, a := range r.State.ActionsOpen {
		if strings.Contains(string(a.Args), `"commit"`) {
			t.Fatalf("an open Commit order survived: %+v", a)
		}
	}
	b := mk(StageBlocked)
	b.Prior, b.Block = StageCommit, state.BlockState{Reason: state.ReasonPublish}
	r = apply(t, b, in(InContinue, nil))
	if r.State.Stage != StageTestReview || !slices.Contains(subtypes(r), "group.resumed") {
		t.Fatalf("blocked in commit: stage %q emits %v", r.State.Stage, subtypes(r))
	}
}

func TestAbandonEndsAnyStageDoneAndBlocksDependents(t *testing.T) {
	for _, st := range []Stage{
		StageIntake,
		StageBuild,
		StageTestReview,
		StageCommit,
		StageCleanup,
		StagePaused,
		StageBlocked,
	} {
		s := mk(st)
		if st == StagePaused || st == StageBlocked {
			s.Prior = StageBuild
		}
		r := apply(t, s, in(InAbandon, AbandonData{Group: "g1"}))
		if r.State.Stage != StageDone || r.State.Outcome != "abandoned" || !slices.Contains(kinds(r), OrderCancel) ||
			!slices.Contains(kinds(r), OrderDiscard) || !slices.Contains(kinds(r), OrderRelease) {
			t.Errorf("%q: stage %q outcome %q orders %v", st, r.State.Stage, r.State.Outcome, kinds(r))
		}
	}
	dep := apply(t, mk(StageBuild), in(InAbandon, AbandonData{Group: "other"}))
	if dep.State.Stage != StageBlocked || dep.State.Block.Reason != state.ReasonDependency {
		t.Fatalf("dependent: stage %q reason %q", dep.State.Stage, dep.State.Block.Reason)
	}
}

func TestLeaseLostEndsTheGroupAndReleasesNothing(t *testing.T) {
	r := apply(t, mk(StageBuild), in(InLeaseLost, nil))
	if r.State.Stage != StageDone || r.State.Outcome != "lost" || slices.Contains(kinds(r), OrderRelease) ||
		!slices.Contains(kinds(r), OrderCancel) {
		t.Fatalf("stage %q outcome %q orders %v", r.State.Stage, r.State.Outcome, kinds(r))
	}
}

func TestBuilderNeedsScopeEndBlocksAndTheFollowingAskIsNotSpentTwice(t *testing.T) {
	end := in(InSessionEnd, SessionEndData{SessionEnd: bus.SessionEnd{
		GroupFacts:  bus.GroupFacts{Reason: "needs_scope"},
		Agent:       "builder",
		Task:        "t1",
		ScopePaths:  []string{"a/b.go"},
		ScopeReason: "need it",
	}})
	r := apply(t, claimed(), end)
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonNeedsScope || !r.State.Tasks[0].ScopeAsked ||
		slices.Contains(kinds(r), OrderScope) {
		t.Fatalf("session.end: stage %q reason %q asked %v orders %v", r.State.Stage, r.State.Block.Reason,
			r.State.Tasks[0].ScopeAsked, kinds(r))
	}

	ask := in(InScopeAsk, ScopeAsk{Task: "t1", Paths: []string{"a/b.go"}, Reason: "need it", Escalation: "esc-1"})
	a := apply(t, r.State, ask)
	if a.State.Stage != StageBlocked || a.State.Block.Reason != state.ReasonNeedsScope || slices.Contains(kinds(a), OrderScope) {
		t.Fatalf("scope.ask: stage %q reason %q orders %v", a.State.Stage, a.State.Block.Reason, kinds(a))
	}
}

func TestUnregisteredFactIsReportedAsDropped(t *testing.T) {
	r := apply(t, mk(StageBuild), in(InFact, FactData{Subtype: "bogus.fact", Data: mustJSON(map[string]string{"a": "b"})}))
	if len(r.Emit) != 0 || len(r.Dropped) != 1 || !strings.Contains(r.Dropped[0], "bogus.fact") ||
		!strings.Contains(r.Dropped[0], "unknown") {
		t.Fatalf("emit %d dropped %q", len(r.Emit), r.Dropped)
	}

	ok := apply(t, mk(StageBuild), in(InFact, FactData{Subtype: "tool.denied"}))
	if len(ok.Dropped) != 0 {
		t.Fatalf("a registered fact was reported as dropped: %q", ok.Dropped)
	}
}

// Each fact the Task Manager and the handlers report with a body of its own becomes one event; a body that the slot does
// not declare is dropped.
func TestFactBodiesFitTheirSlots(t *testing.T) {
	RegisterEvents()
	wake := time.Unix(1_800_000_000, 0).UTC()
	item := map[string]string{"kind": "temp", "name": "/tmp/x", "reason": ""}
	usage := map[string]any{
		"link": "claude-cli", "agent": "builder", "session": "s-1", "model": "m",
		"tokens_in": 1, "tokens_out": 2, "cache_read": 0, "cache_write": 0, "usd": 0.1, "usd_equiv": 0.1,
	}
	facts := map[string]any{
		"command.dropped":     map[string]string{"kind": "continue", "re": "req-1", "why": "no pause to leave"},
		"sandbox.warning":     map[string]string{"warning": "server started without a network filter"},
		"session.resume-lost": map[string]string{"session": "s-1", "agent": "builder", "error": "session expired"},
		"scope.request":       map[string]any{"task": "t1", "paths": []string{"a/b.go"}},
		"scope.answer":        map[string]any{"action": "approve", "approved": []string{"a/b.go"}},
		"group.pause":         map[string]any{"link": "claude-cli", "wait_until": wake, "agent": "builder"},
		"group.resume":        map[string]any{"reason": "timer", "owed": []string{"builder"}},
		"group.stop":          map[string]string{"cause": "pause_timeout"},
		"group.closed":        nil,
		"group.approved":      map[string]string{},
		"provider.skip":       map[string]string{"from": "claude-cli", "to": "anthropic-api", "reason": "unavailable"},
		"session.idle":        map[string]any{"idle_s": 600, "nudged": true, "session": "s-1"},
		"session.timewarn":    map[string]any{"used_s": 3300, "wall_s": 3600, "session": "s-1"},
		"limit.warn":          map[string]any{"kind": "overage", "session": "s-1"},
		"finding.submit":      map[string]any{"rule": "r1", "file": "a.go", "line": 3, "quote": "q", "note": "n"},
		"tool.denied":         map[string]any{"tool": "edit", "row": "B1", "target": "a.go", "count": 1},
		"tool.outside":        map[string]string{"server": "docs", "tool": "search", "outcome": "ok"},
		"sandbox.fallback":    map[string]string{"role": "tester", "wanted": "docker", "used": "seatbelt", "why": "none"},
		"usage":               usage,
		"judge.reset":         map[string]any{"edited": []string{"a/b.go"}},
		"cleanup.swept":       map[string]any{"group": "", "removed": []map[string]string{item}, "kept": nil},
	}
	for sub, body := range facts {
		var raw json.RawMessage
		if body != nil {
			raw = mustJSON(body)
		}
		r := apply(t, mk(StageBuild), in(InFact, FactData{Subtype: sub, Data: raw}))
		if len(r.Emit) != 1 || len(r.Dropped) != 0 || string(r.Emit[0].Subtype) != sub {
			t.Errorf("%s: emitted %d, dropped %q", sub, len(r.Emit), r.Dropped)
		}
	}
}

func TestScopeRules(t *testing.T) {
	ask := func(deny string) Input {
		return in(InScopeAsk, ScopeAsk{Task: "t1", Paths: []string{"a/b.go"}, Reason: "need it", Deny: deny})
	}
	r := apply(t, claimed(), ask("protected"))
	if r.State.Stage != StageBuild || !r.State.Tasks[0].ScopeAsked || !slices.Contains(kinds(r), OrderScope) {
		t.Fatalf("denied: stage %q asked %v orders %v", r.State.Stage, r.State.Tasks[0].ScopeAsked, kinds(r))
	}
	r2 := apply(t, r.State, ask(""))
	if r2.State.Stage != StageBuild || !slices.Contains(kinds(r2), OrderScope) {
		t.Fatalf("second request: stage %q orders %v", r2.State.Stage, kinds(r2))
	}
	r = apply(t, claimed(), ask(""))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonNeedsScope || !r.State.Tasks[0].ScopeAsked {
		t.Fatalf("asked: stage %q reason %q", r.State.Stage, r.State.Block.Reason)
	}
	if r.State.Counters.Blocks["flap"] != 0 {
		t.Fatalf("a scope block counted as a flap")
	}
	approve := in(InScopeAnswer, ScopeAnswer{Answer: "approve", Paths: []string{"a/b.go"}})
	a := apply(t, r.State, approve)
	if a.State.Stage != StageBuild || !slices.Contains(a.State.ScopeAdded, "a/b.go") ||
		!slices.Contains(kinds(a), OrderScope) || !slices.Contains(kinds(a), OrderEnterStage) {
		t.Fatalf("approved: stage %q added %v orders %v", a.State.Stage, a.State.ScopeAdded, kinds(a))
	}
}

func TestWallBlockAnswers(t *testing.T) {
	s := mk(StageBlocked)
	s.Prior = StageBuild
	s.Block = state.BlockState{Reason: state.ReasonWall}
	s.Counters.WallUsedS, s.Counters.WallExtended = 7200, true
	a := apply(t, s, in(InScopeAnswer, ScopeAnswer{Answer: "approve"}))
	if a.State.Stage != StageBuild || a.State.Counters.WallUsedS != 0 || a.State.Counters.WallExtended {
		t.Fatalf("approve: %q %+v", a.State.Stage, a.State.Counters)
	}
	d := Next(s, in(InScopeAnswer, ScopeAnswer{Answer: "deny"}), fixtureLimits())
	if d.State.Stage != StageBlocked {
		t.Fatalf("deny left stage %q", d.State.Stage)
	}
}

func TestTimerExtensionGrantedOnce(t *testing.T) {
	req := in(InTimerExtend, map[string]any{"session": "s1", "used": 1, "limit": int64(30 * time.Minute)})
	req.Request = "m1"
	r := apply(t, claimed(), req)
	if !r.State.Counters.WallExtended || len(r.Orders) != 1 || r.Orders[0].Kind != OrderExtend || r.Orders[0].Re != "m1" {
		t.Fatalf("first: %+v", r.Orders)
	}
	if !strings.Contains(string(r.Orders[0].Data), "1800000000000") {
		t.Fatalf("extension data %s", r.Orders[0].Data)
	}
	req2 := req
	req2.Request = "m2"
	r2 := apply(t, r.State, req2)
	if strings.Contains(string(r2.Orders[0].Data), "1800000000000") {
		t.Fatalf("a second extension was granted: %s", r2.Orders[0].Data)
	}
}

func TestPanicBlocksOnlyThatGroupCrash(t *testing.T) {
	r := apply(t, mk(StageBuild), in(InGroupCrash, nil))
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonCrash {
		t.Fatalf("stage %q reason %q", r.State.Stage, r.State.Block.Reason)
	}
}

func TestFaultRows(t *testing.T) {
	cases := []struct {
		stage  Stage
		reason string
		want   state.Reason
	}{
		{StageBuild, "no_provider", state.ReasonNoProvider},
		{StageIntake, "setup", state.ReasonSetup},
		{StageCommit, "publish", state.ReasonPublish},
		{StageCommit, "merge_conflict", state.ReasonMergeConflict},
		{StageCleanup, "cleanup", state.ReasonCleanup},
		{StageTestReview, "flaky", state.ReasonFlaky},
		{StageTestReview, "tester", state.ReasonTester}, {StageBuild, "context", state.ReasonContext},
	}
	for _, c := range cases {
		r := apply(t, mk(c.stage), in(InFault, FaultData{Reason: c.reason, Detail: "d"}))
		if r.State.Stage != StageBlocked || r.State.Block.Reason != c.want || r.State.Prior != c.stage {
			t.Errorf("%s in %q: stage %q reason %q", c.reason, c.stage, r.State.Stage, r.State.Block.Reason)
		}
	}
	if r := Next(mk(StageBuild), in(InFault, FaultData{Reason: "cleanup"}), fixtureLimits()); !r.Refused {
		t.Errorf("a cleanup fault in Build was applied")
	}
}

func TestTesterWithoutAResultRestartsWithinItsRetries(t *testing.T) {
	s := failedRound()
	end := in(InSessionEnd, SessionEndData{
		SessionEnd: bus.SessionEnd{GroupFacts: bus.GroupFacts{Reason: "no_result"}, Agent: "tester"},
	})
	r := apply(t, s, end)
	if r.State.TestReview.Tester.Restarts != 1 || r.State.Stage != StageTestReview ||
		!slices.Contains(kinds(r), OrderEnterStage) || r.State.Counters.Returns != 0 {
		t.Fatalf("restarts %d stage %q orders %v", r.State.TestReview.Tester.Restarts, r.State.Stage, kinds(r))
	}
	r = apply(t, r.State, end)
	if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonRetries {
		t.Fatalf("stage %q reason %q", r.State.Stage, r.State.Block.Reason)
	}
}

func TestUnfinishedEditsAreRecordedAndClearedAtTaskDone(t *testing.T) {
	end := in(InSessionEnd, SessionEndData{SessionEnd: bus.SessionEnd{
		GroupFacts: bus.GroupFacts{Reason: "timeout"},
		Agent:      "builder",
		Task:       "t1",
	},
		Unfinished: &state.UnfinishedEdits{Task: "t1", Files: []string{"a.go"}}})
	r := apply(t, claimed(), end)
	if r.State.Unfinished == nil || r.State.Unfinished.Task != "t1" || r.State.Unfinished.Files[0] != "a.go" {
		t.Fatalf("unfinished = %+v", r.State.Unfinished)
	}
	var o Order
	for _, x := range r.Orders {
		if x.Kind == OrderEnterStage {
			o = x
		}
	}
	if !strings.Contains(string(o.Data), "a.go") {
		t.Fatalf("the stage order does not carry the unfinished files: %s", o.Data)
	}
	done := apply(t, r.State, in(InTaskDone, bus.TaskDone{Task: "t1"}))
	if done.State.Unfinished != nil || done.State.Tasks[0].State != "done" {
		t.Fatalf("after task.done: unfinished %+v task %+v", done.State.Unfinished, done.State.Tasks[0])
	}
}

func TestCrashSessionEndChargesNoAttemptAndOrdersTheStageAgain(t *testing.T) {
	s := claimed()
	s.Counters.Attempts = 1
	s.Resume = []state.ResumeRef{{Agent: "builder", Session: "s1", Handle: "h9"}}
	r := apply(t, s, builderEnd("crash"))
	c := r.State.Counters
	if c.Attempts != 1 || c.Retries != 0 || c.Crashes != 1 || r.State.Stage != StageBuild || r.State.Tasks[0].Session != "" {
		t.Fatalf("counters %+v stage %q", c, r.State.Stage)
	}
	if len(r.Orders) != 1 || r.Orders[0].Kind != OrderEnterStage || len(r.Orders[0].Resume) != 1 ||
		r.Orders[0].Resume[0].Handle != "h9" {
		t.Fatalf("orders %+v", r.Orders)
	}
}

func TestBilling(t *testing.T) {
	r := apply(t, claimed(), builderEnd("billing"))
	if r.State.Block.Reason != state.ReasonPaidRefused {
		t.Fatalf("reason %q", r.State.Block.Reason)
	}
}

func TestSpendingPauseContinueClearsPause(t *testing.T) {
	s := mk(StagePaused)
	s.Prior = StageBuild
	s.Pause = state.PauseState{Cause: "spending", Since: t0}
	r := apply(t, s, in(InContinue, nil))
	if r.State.Stage != StageBuild || r.State.Pause.Cause != "" || !r.State.Pause.Since.IsZero() {
		t.Fatalf("stage %q pause %+v", r.State.Stage, r.State.Pause)
	}
	if r.State.Epoch != s.Epoch {
		t.Fatalf("epoch changed to %d", r.State.Epoch)
	}
}

func TestBlockedFlagsRedesignAtThreeBlocks(t *testing.T) {
	s := mk(StageBuild)
	s.Counters.Blocks["crash"] = 2
	r := apply(t, s, in(InGroupCrash, nil))
	var detail string
	for _, e := range r.Emit {
		if b, ok := e.Data.(*bus.GroupBlocked); ok {
			detail = b.Detail
		}
	}
	if !strings.Contains(detail, "needs redesign") {
		t.Fatalf("detail %q", detail)
	}
}

// apply runs Next and returns the new state, failing the test on a refusal.
func apply(t *testing.T, s State, i Input) Result {
	t.Helper()
	r := Next(s, i, fixtureLimits())
	if r.Refused {
		t.Fatalf("%s in %q was refused", i.Kind, s.Stage)
	}
	return r
}

// fixtureLimits are the limits every proof case uses.
func fixtureLimits() Limits {
	return Limits{
		Returns:         2,
		GroupAttempts:   2,
		PauseMaxMin:     360,
		GroupWallMin:    60,
		BuilderRetries:  1,
		ReviewerRetries: 1,
		TesterRetries:   1,
		PoolMin:         1,
		PoolMax:         2,
	}
}

// kinds lists the order kinds of a result.
func kinds(r Result) []OrderKind {
	var k []OrderKind
	for _, o := range r.Orders {
		k = append(k, o.Kind)
	}
	return k
}

// subtypes lists the event subtypes of a result.
func subtypes(r Result) []string {
	var s []string
	for _, e := range r.Emit {
		s = append(s, string(e.Subtype))
	}
	return s
}

// builderEnd builds a builder session.end input.
func builderEnd(reason string) Input {
	return in(InSessionEnd, SessionEndData{
		SessionEnd: bus.SessionEnd{GroupFacts: bus.GroupFacts{Reason: reason}, Agent: "builder", Task: "t1", Tokens: 100},
	})
}

// in builds an input one minute after t0 plus the given offset.
func in(kind InputKind, d any) Input {
	i := Input{Kind: kind, Group: "g1", Time: t0.Add(time.Minute)}
	if d != nil {
		i.Data = data(d)
	}
	return i
}

// data marshals v as input data.
func data(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// claimed returns a Build state whose task t1 is claimed by a live builder session.
func claimed() State {
	s := mk(StageBuild)
	s.Tasks[0].State, s.Tasks[0].Session = "claimed", "s1"
	return s
}

// mk returns a valid group state in the given stage with two queued tasks.
func mk(stage Stage) State {
	s := State{
		Run: "r1", Group: "g1", Stage: stage, Epoch: 3, Fence: 7, ConfigHash: strings.Repeat("a", 64),
		Counters:    state.Counters{Blocks: map[string]int{}},
		Tasks:       []state.TaskState{{ID: "t1", State: "queued"}, {ID: "t2", State: "queued"}},
		TestReview:  state.TestReview{Findings: []state.Finding{}, NotMet: []string{}, Failed: []string{}},
		ActionsOpen: []state.OpenAction{},
		Resume:      []state.ResumeRef{},
		Owed:        []string{},
		Requests:    []string{},
		ScopeAdded:  []string{},
		LastEventAt: t0,
	}
	if stage == StageDone {
		s.Outcome = "complete"
	}
	return s
}

// stateEqual compares two states by their JSON form.
func stateEqual(a, b State) bool { return string(data(a)) == string(data(b)) }

// failedRound returns a Test and Review state in which the tester has failed.
func failedRound() State {
	s := mk(StageTestReview)
	s.Tasks[0].State, s.Tasks[1].State = "done", "done"
	s.TestReview.Tester = state.RoundState{Session: "st", Result: ""}
	s.TestReview.Reviewer = state.RoundState{Session: "sr", Result: ""}
	return s
}

func verifyEnd(agent string, pass bool, failed ...string) Input {
	return in(InSessionEnd, SessionEndData{SessionEnd: bus.SessionEnd{GroupFacts: bus.GroupFacts{
		Reason: "done",
	}, Agent: agent,
		Verify: &bus.VerifyOutcome{Pass: pass, Failed: failed}}})
}
