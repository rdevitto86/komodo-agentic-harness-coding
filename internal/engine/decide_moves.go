package engine

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// faultStages lists, for each reason a fault may carry, the stages where it blocks the group.
var faultStages = map[state.Reason][]Stage{
	state.ReasonNoProvider:    {StageBuild, StageTestReview},
	state.ReasonPaidRefused:   {StageBuild, StageTestReview},
	state.ReasonContext:       {StageIntake, StageBuild, StageTestReview},
	state.ReasonSetup:         {StageIntake},
	state.ReasonPublish:       {StageCommit},
	state.ReasonMergeConflict: {StageCommit},
	state.ReasonCleanup:       {StageCleanup},
	state.ReasonFlaky:         {StageTestReview},
	state.ReasonTester:        {StageTestReview},
	state.ReasonRegression:    {StageTestReview},
	state.ReasonReturns:       {StageTestReview},
	state.ReasonFlap:          {StageTestReview},
	state.ReasonAttempts:      {StageBuild},
	state.ReasonRetries:       {StageBuild, StageTestReview},
	state.ReasonWall:          {StageBuild},
	state.ReasonCrash: {
		StageIntake,
		StageBuild,
		StageTestReview,
		StageCommit,
		StageCleanup,
		StagePaused,
	},
	state.ReasonContract: {
		StageIntake,
		StageBuild,
		StageTestReview,
		StageCommit,
		StageCleanup,
		StagePaused,
	},
	state.ReasonStateInvalid: {
		StageIntake,
		StageBuild,
		StageTestReview,
		StageCommit,
		StageCleanup,
		StagePaused,
	},
}

// move applies the stage-move rows: the first row that matches the input and the state.
func (c *calc) move() bool {
	in, s := c.eff, &c.s
	if r, d, ok := c.blockReason(in); ok {
		c.block(r, d)
		return true
	}

	switch in.Kind {
	case InGroupStart:
		if c.from == NoStage {
			s.Epoch = 1
			s.Stage = StageIntake
			c.emitStatus(&bus.StageEnter{To: string(StageIntake)})
			c.order(OrderGrantLease, StageIntake, nil)
			return true
		}
	case InLeaseGranted:
		if c.from == StageIntake {
			if g := decode[LeaseGrant](in.Data); g.Fence != 0 {
				s.Fence = g.Fence
				c.order(OrderStartGroup, StageIntake, nil)
				return true
			}
		}
	case InReady:
		if c.from == StageIntake {
			c.advance(StageBuild, stageData{Task: c.firstOpenTask()})
			return true
		}
	case InStageDone:
		return c.stageDone(in)
	case InResultFail:
		if c.from == StageTestReview {
			c.returnToBuild(in)
			return true
		}
	case InSessionEnd:
		return c.pauseOnEnd(in)
	case InLimitPause:
		if (c.from == StageBuild || c.from == StageTestReview) && !c.anyLive() {
			d := decode[LimitPause](in.Data)
			s.Prior, s.Stage = c.from, StagePaused
			s.Pause = state.PauseState{
				Cause:   d.Cause,
				Link:    d.Link,
				Until:   d.Until,
				Since:   in.Time,
				Refused: s.Pause.Refused,
			}
			c.emitStatus(&bus.GroupPaused{
				GroupFacts: bus.GroupFacts{Stage: string(c.from)},
				Until:      d.Until,
				Link:       d.Link,
			})
			c.order(OrderHold, StagePaused, nil)
			return true
		}
	case InTimerDue:
		if c.from == StagePaused && decode[TimerDue](in.Data).Reason != "pause_timeout" {
			c.leavePause(decode[TimerDue](in.Data).Reason)
			return true
		}
	case InContinue:
		return c.continueGroup(in)
	case InScopeAnswer:
		return c.scopeAnswer(in)
	case InLeaseLost:
		if c.from != StageDone && c.from != NoStage {
			c.emit = append(
				c.emit,
				c.event(
					bus.TypeRunFailure,
					&bus.LeaseLost{
						GroupFacts: bus.GroupFacts{Stage: string(c.from)},
						Fence:      s.Fence,
						Where:      "renew",
					},
				),
			)
			c.order(OrderCancel, c.from, map[string]string{"cause": "lease_lost"})
			c.finish("lost")
			return true
		}
	case InAbandon:
		if g := decode[AbandonData](in.Data).Group; (g == "" || g == s.Group) && c.from != StageDone && c.from != NoStage {
			c.order(OrderCancel, c.from, map[string]string{"cause": "abandon"})
			c.order(OrderDiscard, StageDone, nil)
			c.finish("abandoned")
			c.order(OrderRelease, StageDone, nil)
			return true
		}
	case InGroupClosed:
		switch c.from {
		case StageCleanup:
			c.finish("complete")
			c.order(OrderRelease, StageDone, nil)
			return true
		case StageBlocked, StageDone:
			return true
		}
	}

	return false
}

// blockReason applies the block rows to an input; it reports the reason, a sentence for a person and whether a row matched.
func (c *calc) blockReason(in Input) (state.Reason, string, bool) {
	s, st := &c.s, c.from
	if st == NoStage || st == StageBlocked || st == StageDone {
		return "", "", false
	}

	switch in.Kind {
	case InResultFail:
		if st != StageTestReview {
			return "", "", false
		}

		d := decode[ResultFail](in.Data)
		if s.Counters.Returns >= c.l.Returns {
			if d.Regression {
				return state.ReasonRegression, "a test that passed at the base fails and the returns are spent", true
			}
			return state.ReasonReturns, "the returns to Build are spent", true
		}
		if (s.FailureHash != "" && Fingerprint(d.Items) == s.FailureHash) || (d.Tree != "" && d.Tree == s.ReturnTree) {
			return state.ReasonFlap, "the same failure came back, or the return changed nothing", true
		}
	case InSessionEnd:
		if r, d, ok := c.endBlock(decode[SessionEndData](in.Data).SessionEnd); ok {
			return r, d, true
		}
	case InScopeAsk:
		if c.scopeBlock != nil {
			return state.ReasonNeedsScope, c.scopeBlock.Reason, true
		}
	case InFault:
		d := decode[FaultData](in.Data)
		r := state.Reason(d.Reason)
		if r == "commit" {
			r = state.ReasonPublish
		}
		if slices.Contains(faultStages[r], st) {
			return r, d.Detail, true
		}
		return "", "", false
	case InGroupCrash:
		if working(st) || st == StagePaused {
			return state.ReasonCrash, "the group's goroutine panicked", true
		}
	case InAbandon:
		if g := decode[AbandonData](in.Data).Group; g != "" && g != s.Group && (working(st) || st == StagePaused) {
			return state.ReasonDependency, "dependency " + g + " was abandoned", true
		}
	case InTimerDue:
		if decode[TimerDue](in.Data).Reason == "pause_timeout" &&
			st == StagePaused &&
			s.Pause.Cause == "provider" &&
			c.l.PauseMaxMin > 0 &&
			!in.Time.Before(s.Pause.Since.Add(time.Duration(c.l.PauseMaxMin)*time.Minute)) {
			return state.ReasonPauseTimeout, "the provider limit outlasted limits.pause_max_min", true
		}
	}

	if st == StageBuild &&
		s.Counters.WallExtended &&
		c.l.GroupWallMin > 0 &&
		s.Counters.WallUsedS >= 2*int64(c.l.GroupWallMin)*60 {
		switch in.Kind {
		case InAbandon, InLeaseLost, InGroupCrash, InContinue, InScopeAnswer:
		default:
			return state.ReasonWall, "working time reached twice limits.group_wall_min", true
		}
	}

	return "", "", false
}

// endBlock applies the block rows that read a session end.
func (c *calc) endBlock(e bus.SessionEnd) (state.Reason, string, bool) {
	s, st := &c.s, c.from
	if st != StageBuild && st != StageTestReview {
		return "", "", false
	}
	if e.Reason == "billing" {
		return state.ReasonPaidRefused, "the first stream line showed a paid key", true
	}

	if e.Agent == "builder" && st == StageBuild {
		open := !c.allTasksDone()
		if (e.Reason == "timeout" || e.Reason == "idle") && open && s.Counters.Retries > c.l.BuilderRetries {
			return state.ReasonRetries, "the builder timed out or went idle more often than its retries", true
		}
		if slices.Contains(retryReasons, e.Reason) &&
			(e.Reason != "done" || open) &&
			s.Counters.Attempts >= c.l.GroupAttempts {
			return state.ReasonAttempts, "every fresh builder session was used", true
		}

		return "", "", false
	}

	if e.Agent == "tester" || e.Agent == "reviewer" {
		round, retries := s.TestReview.Tester, c.l.TesterRetries
		if e.Agent == "reviewer" {
			round, retries = s.TestReview.Reviewer, c.l.ReviewerRetries
		}

		silent := !slices.Contains(
			[]string{
				"cancelled",
				"pause_timeout",
				"crash",
				"provider",
				"paused",
				"needs_scope",
				"billing",
			},
			e.Reason,
		) && (e.Reason != "done" || e.Verify == nil)
		if silent && round.Restarts > retries {
			return state.ReasonRetries, "the " + e.Agent + " ended without a result more often than its retries", true
		}
	}

	return "", "", false
}

// block moves the group to Blocked for a reason and cancels its live sessions.
func (c *calc) block(r state.Reason, detail string) {
	s := &c.s
	if working(c.from) {
		s.Prior = c.from
	}

	s.Stage = StageBlocked
	s.Block = state.BlockState{Reason: r, At: c.in.Time}
	s.Pause.After = false
	s.Counters.Blocks[string(r)]++
	c.order(OrderCancel, s.Stage, map[string]string{"cause": "block"})

	b := &bus.GroupBlocked{
		GroupFacts: bus.GroupFacts{Stage: string(s.Prior), Reason: string(r)},
		Prior:      string(s.Prior),
		Detail:     detail,
	}
	if c.scopeBlock != nil && r == state.ReasonNeedsScope {
		b.Paths = c.scopeBlock.Paths
	}
	if s.Counters.Blocks[string(r)] >= BlocksBeforeRedesign {
		b.Detail = strings.TrimSpace(b.Detail + " (needs redesign)")
	}

	c.emit = append(c.emit, c.event(bus.TypeRunFailure, b))
}

// advance enters a working stage: the epoch rises and the stage's order goes out.
func (c *calc) advance(to Stage, d stageData) {
	s := &c.s
	s.Epoch++
	s.Stage = to

	s.Pause = state.PauseState{}
	if to == StageTestReview {
		s.TestReview.Tester, s.TestReview.Reviewer = state.RoundState{}, state.RoundState{}
		s.TestReview.Failed, s.TestReview.NotMet = []string{}, []string{}
	}

	c.emitStatus(&bus.StageEnter{From: string(c.from), To: string(to)})
	c.enterStage(to, d)
}

// stageDone moves a group on when its stage finished: rows 4, 5 and 7.
func (c *calc) stageDone(in Input) bool {
	d := decode[StageDoneData](in.Data)
	if d.Stage != "" && d.Stage != c.from {
		return false
	}

	switch c.from {
	case StageBuild:
		if c.allTasksDone() {
			c.advance(StageTestReview, stageData{})
			return true
		}
	case StageTestReview:
		c.advance(StageCommit, stageData{})
		return true
	case StageCommit:
		c.advance(StageCleanup, stageData{})
		return true
	}

	return false
}

// returnToBuild sends a failed Test and Review round back to Build with its findings (row 6).
func (c *calc) returnToBuild(in Input) {
	s := &c.s
	d := decode[ResultFail](in.Data)
	s.Counters.Returns++
	s.FailureHash, s.ReturnTree = Fingerprint(d.Items), d.Tree
	if c.anyLive() {
		c.order(OrderCancel, StageTestReview, map[string]string{"cause": "return"})
	}

	findings := d.Findings
	if findings == "" {
		findings = strings.Join(d.Items, "\n")
	}

	s.TestReview.Failed = slices.Clone(d.Items)

	c.emitStatus(&bus.StageEnter{From: string(StageTestReview), To: string(StageBuild), Return: true})

	s.Epoch++
	s.Stage = StageBuild
	s.TestReview.Tester, s.TestReview.Reviewer = state.RoundState{}, state.RoundState{}

	c.enterStage(StageBuild, stageData{Task: c.firstOpenTask(), Findings: findings, Items: d.Items})
}

// pauseOnEnd pauses a group when a session ends: rows 18 and 9.
func (c *calc) pauseOnEnd(in Input) bool {
	s := &c.s
	if c.from != StageBuild && c.from != StageTestReview {
		return false
	}

	e := decode[SessionEndData](in.Data).SessionEnd
	switch {
	case s.Pause.After && !c.anyLive():
		s.Prior, s.Stage = c.from, StagePaused

		s.Pause.After = false
		if s.Pause.Since.IsZero() {
			s.Pause.Since = in.Time
		}

		c.emitStatus(&bus.GroupPaused{
			GroupFacts: bus.GroupFacts{Stage: string(c.from)},
			Until:      s.Pause.Until,
			Link:       s.Pause.Link,
		})
		c.order(OrderHold, StagePaused, nil)
		return true
	case e.Reason == "paused":
		p := &s.Pause
		if p.Since.IsZero() {
			p.Since, p.Refused = in.Time, 0
		} else {
			p.Refused++
		}

		p.Cause, p.Link = "provider", e.Link
		if !e.ResetAt.IsZero() {
			p.Until = e.ResetAt.Add(time.Minute)
		} else {
			waits := []time.Duration{15 * time.Minute, 30 * time.Minute, 60 * time.Minute}
			p.Until = in.Time.Add(waits[min(max(p.Refused-1, 0), len(waits)-1)])
		}

		s.Prior, s.Stage = c.from, StagePaused
		c.emitStatus(&bus.GroupPaused{
			GroupFacts: bus.GroupFacts{Stage: string(c.from)},
			Until:      p.Until,
			Link:       p.Link,
		})

		return true
	}

	return false
}

// leavePause returns a paused group to the stage it left, in the same epoch (rows 10 and 12).
func (c *calc) leavePause(wake string) {
	s := &c.s
	to := s.Prior
	if wake == "" {
		wake = "timer"
	}
	if s.Pause.Cause != "provider" {
		s.Pause = state.PauseState{}
	} else {
		s.Pause.Until, s.Pause.After = time.Time{}, false
	}

	s.Stage, s.Prior = to, ""
	c.emitStatus(&bus.GroupResumed{From: string(StagePaused), To: string(to)})
	c.enterStage(to, stageData{Task: c.firstOpenTask(), Wake: wake})

	for _, a := range s.Owed {
		c.enterStage(to, stageData{Agent: a})
	}

	s.Owed = []string{}
}

// continueGroup applies a continue to a Paused, Blocked or Commit group (rows 12, 13 and 19).
func (c *calc) continueGroup(in Input) bool {
	s := &c.s
	switch c.from {
	case StagePaused:
		if s.Prior == StageCommit {
			c.resumeAt(StageTestReview)
			return true
		}
		c.leavePause("run-continue")
		return true
	case StageBlocked:
		if why := c.resetFor(s.Block.Reason); why != "" {
			c.refuseRequest(in, why)
			return false
		}

		to := s.Prior
		if to == StageCommit {
			to = StageTestReview
		}

		c.resumeAt(to)
		return true
	case StageCommit:
		c.dropCommitOrders()
		c.advance(StageTestReview, stageData{})
		return true
	}

	c.refuseRequest(in, "the group is running")
	return false
}

// resumeAt leaves Paused or Blocked into a working stage with the epoch raised (rows 13, 14 and 19).
func (c *calc) resumeAt(to Stage) {
	s := &c.s

	held := string(c.from)
	if s.Prior == StageCommit {
		c.dropCommitOrders()
	}

	s.Stage, s.Prior = to, ""
	s.Block, s.Pause = state.BlockState{}, state.PauseState{}
	s.Epoch++

	c.emitStatus(&bus.GroupResumed{From: held, To: string(to)})
	c.emitStatus(&bus.StageEnter{From: held, To: string(to)})
	c.enterStage(to, stageData{Task: c.firstOpenTask()})
}

// dropCommitOrders removes the open orders of the Commit stage; Commit runs again after approval.
func (c *calc) dropCommitOrders() {
	c.s.ActionsOpen = slices.DeleteFunc(c.s.ActionsOpen, func(a state.OpenAction) bool {
		var o Order
		return json.Unmarshal(a.Args, &o) == nil && o.Stage == StageCommit
	})
}

// resetFor clears the counters a block reason names; it returns a sentence when the continue must be refused.
func (c *calc) resetFor(r state.Reason) string {
	s := &c.s

	switch r {
	case state.ReasonReturns, state.ReasonRegression:
		s.Counters.Returns, s.FailureHash, s.ReturnTree = 0, "", ""
	case state.ReasonFlap:
		s.FailureHash, s.ReturnTree = "", ""
	case state.ReasonRetries:
		s.Counters.Retries = 0
		s.TestReview.Tester.Restarts, s.TestReview.Reviewer.Restarts = 0, 0
	case state.ReasonAttempts:
		s.Counters.Attempts = 0
	case state.ReasonWall:
		s.Counters.WallUsedS, s.Counters.WallExtended = 0, false
	case state.ReasonPauseTimeout:
		s.Pause.Since, s.Pause.Refused = time.Time{}, 0
	case state.ReasonNeedsScope:
		return "the group waits for an answer: run it with --approve or --deny"
	}

	return ""
}

// refuseRequest queues the request.refused fact for a command that matched no row.
func (c *calc) refuseRequest(in Input, why string) {
	if in.Request != "" {
		c.refuse = &bus.RequestRefused{RequestRef: bus.RequestRef{Request: bus.ID(in.Request)}, Why: why}
	}
}

// scopeAnswer applies the answer to a scope request or a wall block (row 14).
func (c *calc) scopeAnswer(in Input) bool {
	s := &c.s
	if c.from != StageBlocked {
		return false
	}

	d := decode[ScopeAnswer](in.Data)

	switch s.Block.Reason {
	case state.ReasonNeedsScope:
		if d.Answer == "approve" {
			s.ScopeAdded = appendNew(s.ScopeAdded, d.Paths...)
		}
		c.order(OrderScope, StageBuild, d)
		c.resumeAt(s.Prior)
		return true
	case state.ReasonWall:
		if d.Answer != "approve" {
			return false
		}
		s.Counters.WallUsedS, s.Counters.WallExtended = 0, false
		c.resumeAt(s.Prior)
		return true
	}

	return false
}

// finish ends the group in Done with an outcome.
func (c *calc) finish(outcome string) {
	s := &c.s
	s.Stage, s.Outcome, s.Prior = StageDone, outcome, ""
	s.Pause, s.Block = state.PauseState{}, state.BlockState{}
	c.emitStatus(&bus.GroupDone{Outcome: outcome})
}

// mustJSON encodes v; the types it is given always encode.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
