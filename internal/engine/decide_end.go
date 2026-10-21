package engine

import (
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// sessionEnd records a session that ended and applies the exit row of its agent.
func (c *calc) sessionEnd() bool {
	s := &c.s
	d := decode[SessionEndData](c.in.Data)
	e := d.SessionEnd
	if e.Stage == "" {
		e.Stage = string(s.Stage)
	}
	if s.Stage == NoStage || s.Stage == StageIntake {
		return false
	}

	reason := e.Reason

	tokens := e.Tokens
	if tokens == 0 && e.Usage != nil {
		tokens = int64(e.Usage.TokensIn + e.Usage.TokensOut)
	}

	s.Counters.TokensUsed += tokens

	c.emitStatus(&e)
	ref := &state.ResumeRef{Agent: e.Agent, Session: string(e.Session), Handle: e.Handle, Link: e.Link}

	keep := reason == "paused" || reason == "provider" || reason == "needs_scope" ||
		(reason == "done" && len(e.ScopePaths) > 0)
	switch {
	case keep && e.Handle != "":
		c.setResume(e.Agent, ref)
	case keep || reason == "crash":
	default:
		c.setResume(e.Agent, nil)
	}

	holds := s.Stage == StagePaused || s.Stage == StageBlocked || s.Stage == StageDone
	switch e.Agent {
	case "builder":
		return c.builderEnd(e, d.Unfinished, holds)
	case "qa", "reviewer":
		return c.verifierEnd(e, holds)
	}

	return true
}

// builderEnd clears the builder's task and applies the exit row of its reason.
func (c *calc) builderEnd(e bus.SessionEnd, unfinished *state.UnfinishedEdits, holds bool) bool {
	s := &c.s
	if t := c.taskByID(e.Task, string(e.Session)); t != nil && t.State == "claimed" {
		t.State, t.Session = "queued", ""
	}
	if unfinished != nil && e.Reason != "done" {
		s.Unfinished = unfinished
	}
	if holds {
		if s.Stage == StagePaused && e.Reason == "paused" {
			c.pauseAgain(e)
		}
		return true
	}
	if s.Stage != StageBuild {
		return false
	}

	switch {
	case state.Reason(e.Reason).Charged():
		c.charge(e.Reason)
	case e.Reason == "crash":
		s.Counters.Crashes++
		c.waive(e.Reason)
	case e.Reason == "provider", e.Reason == "cancelled":
		c.waive(e.Reason)
	}
	if s.Pause.After {
		return true
	}

	switch e.Reason {
	case "done":
		if len(e.ScopePaths) > 0 {
			// The Task Manager denied this request itself and resumed the builder; the task's one ask is spent.
			if len(s.Tasks) > 0 {
				s.Tasks[c.taskIndex(e.Task)].ScopeAsked = true
			}
		} else if !c.allTasksDone() && c.charge("unfinished") {
			c.enterStage(StageBuild, stageData{Task: c.firstOpenTask(), Retry: e.Reason})
		}
	case "check_fail", "no_result", "schema_fail", "refusals", "timeout", "idle":
		if !c.builderBlocked(e.Reason) {
			c.enterStage(StageBuild, stageData{Task: c.firstOpenTask(), Retry: e.Reason})
		}
	case "provider", "crash":
		if !c.builderBlocked(e.Reason) {
			c.enterStage(StageBuild, stageData{Task: c.firstOpenTask(), Retry: e.Reason})
		}
	case "needs_scope":
		// The block rows block the group; the scope.ask that follows finds the ask spent and attaches to that block.
		c.scopeRules(ScopeAsk{Task: e.Task, Paths: e.ScopePaths, Reason: e.ScopeReason})
	}

	return true
}

// pauseAgain records that a sibling session of a paused group halted too.
func (c *calc) pauseAgain(e bus.SessionEnd) {
	p := &c.s.Pause
	if until := e.ResetAt.Add(time.Minute); !e.ResetAt.IsZero() && until.After(p.Until) {
		p.Until = until
	}
	p.Refused++
}

// builderBlocked reports whether the retry the exit asks for is already over a bound.
func (c *calc) builderBlocked(reason string) bool {
	_, _, ok := c.blockReason(c.in)
	return ok
}

// taskIndex returns the index of a task, or 0 when the identifier is unknown.
func (c *calc) taskIndex(id string) int {
	for i, t := range c.s.Tasks {
		if t.ID == id {
			return i
		}
	}
	return 0
}

// scopeRules denies a request that is protected, belongs to another group or repeats the task's one request; otherwise it
// blocks the group.
func (c *calc) scopeRules(a ScopeAsk) {
	s := &c.s
	i := c.taskIndex(a.Task)
	if len(s.Tasks) == 0 {
		return
	}

	t := &s.Tasks[i]
	spent := t.ScopeAsked
	t.ScopeAsked = true

	if spent || a.Deny != "" {
		why := a.Deny
		if spent {
			why = "spent"
		}

		o := c.order(OrderScope, StageBuild, ScopeAnswer{Answer: "deny", Paths: a.Paths, Reason: why})
		o.Re = a.Escalation
		if o.Re == "" {
			o.Re = c.in.Request
		}

		return
	}

	c.scopeBlock = &a
}

// verifierEnd records a Quality Assurance or reviewer that ended and restarts it when it gave no result.
func (c *calc) verifierEnd(e bus.SessionEnd, holds bool) bool {
	s := &c.s
	round := &s.TestReview.QA
	if e.Agent == "reviewer" {
		round = &s.TestReview.Reviewer
	}
	if s.Stage == StageDone || (s.Stage != StageTestReview && !holds) {
		return s.Stage == StageDone
	}

	round.Session = ""

	switch {
	case e.Reason == "done" && e.Verify != nil:
		round.Result = "fail"
		if e.Verify.Pass {
			round.Result = "pass"
		}

		s.TestReview.Failed = appendNew(s.TestReview.Failed, e.Verify.Failed...)

		s.TestReview.NotMet = appendNew(s.TestReview.NotMet, e.Verify.NotMet...)
		if s.Stage == StageTestReview {
			c.settleRound()
		}
	case e.Reason == "cancelled" || e.Reason == "pause_timeout":
		round.Result = "cancelled"
	case e.Reason == "paused" && s.Stage == StagePaused:
		c.pauseAgain(e)
	case e.Reason == "done":
		// A judge that ended done gave its result; the Task Manager reports the round, so nothing is charged or restarted.
	case e.Reason == "billing" || e.Reason == "paused" || e.Reason == "needs_scope":
	case e.Reason == "crash":
		s.Counters.Crashes++
		c.waive(e.Reason)
		if !c.faultCapped() {
			c.restartAgent(e.Agent)
		}
	case e.Reason == "provider":
		c.waive(e.Reason)
		c.restartAgent(e.Agent)
	default:
		if !c.charge(e.Reason) {
			return true
		}
		if s.Stage == StagePaused || s.Pause.After {
			s.Owed = appendNew(s.Owed, e.Agent)
		} else {
			c.restartAgent(e.Agent)
		}
	}

	return true
}

// settleRound makes the stage-move rows read the round's outcome once both agents have a result.
func (c *calc) settleRound() {
	t, r := c.s.TestReview.QA.Result, c.s.TestReview.Reviewer.Result
	if t == "" || r == "" || t == "cancelled" || r == "cancelled" {
		return
	}
	if t == "pass" && r == "pass" {
		c.eff = Input{
			Kind:  InStageDone,
			Group: c.in.Group,
			Time:  c.in.Time,
			Data:  mustJSON(StageDoneData{Stage: StageTestReview}),
		}
		return
	}

	items := slices.Clone(c.s.TestReview.Failed)
	items = appendNew(items, c.s.TestReview.NotMet...)
	for _, f := range c.s.TestReview.Findings {
		items = appendNew(items, f.Rule+" "+f.File)
	}

	c.eff = Input{Kind: InResultFail, Group: c.in.Group, Time: c.in.Time, Data: mustJSON(ResultFail{Items: items})}
}

// restartAgent orders a fresh session of one agent on the group's tree, or owes it while the group is paused.
func (c *calc) restartAgent(agent string) {
	if c.s.Stage == StagePaused || c.s.Pause.After {
		c.s.Owed = appendNew(c.s.Owed, agent)
		return
	}
	c.enterStage(StageTestReview, stageData{Agent: agent})
}

// scopeAsk applies the scope rules to a builder's request for more files.
func (c *calc) scopeAsk() bool {
	// A builder's session end already blocked the group and spent the ask; this ask carries the escalation the answer replies to.
	if c.s.Stage == StageBlocked && c.s.Block.Reason == state.ReasonNeedsScope {
		return true
	}
	if c.s.Stage != StageBuild {
		return false
	}
	c.scopeRules(decode[ScopeAsk](c.in.Data))
	return true
}

// appendNew appends the values that the list does not hold yet.
func appendNew(list []string, vals ...string) []string {
	for _, v := range vals {
		if !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}
