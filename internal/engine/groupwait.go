package engine

import (
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// The per-group breaker's waits: the nth blocker error in a row holds the group's next session start back by one of these.
// [TODO: tune the per-group waits from error counts (G-294)]
const (
	groupWaitFirst  = time.Minute
	groupWaitSecond = 2 * time.Minute
	groupWaitThird  = 4 * time.Minute
)

// groupWait returns how long a group waits before its next session start when it has had streak blocker errors in a row; the
// streak that reaches the cap blocks the group instead, so it waits 0.
func groupWait(streak int) time.Duration {
	switch streak {
	case 1:
		return groupWaitFirst
	case 2:
		return groupWaitSecond
	case 3:
		return groupWaitThird
	}

	return 0
}

// holdFor holds the group's session starts back for wait and asks for them again when it ends; 0 leaves a hold alone.
func (g *group) holdFor(wait time.Duration) {
	if wait <= 0 {
		return
	}

	g.clearHold()

	g.holdUntil = g.now().Add(wait)
	g.blockWait = g.st.Stage == StageBlocked
	g.holdT = g.m.clk.AfterFunc(wait, func() { g.enqueue(Input{Kind: InHoldDue, Group: g.spec.ID}) })
}

// holding reports whether the group's own wait still holds its session starts back.
func (g *group) holding() bool { return g.now().Before(g.holdUntil) }

// clearHold ends the group's wait and stops its timer.
func (g *group) clearHold() {
	if g.holdT != nil {
		g.holdT.Stop()
		g.holdT = nil
	}

	g.holdUntil = time.Time{}
	g.blockWait = false
}

// waitingBlock reports whether a state is blocked by a blocker error that has not reached the cap: its wait ends and the
// group retries, so no person is asked.
func waitingBlock(s State) bool {
	return s.Stage == StageBlocked &&
		state.Classify(s.Block.Reason) == state.SeverityBlocker &&
		s.Counters.FaultStreak < faultCap
}

// keepWait keeps the wait of a group blocked by a blocker error, ends the wait of a group that left that block or stopped
// working, and tells the run whether the group retries by itself.
func (g *group) keepWait(stage Stage) {
	keep := stage == StageBlocked && g.holdT != nil && waitingBlock(g.st)
	if !keep && (g.blockWait || stage == StagePaused || stage == StageBlocked || stage == StageDone) {
		g.clearHold()
	}

	g.mu.Lock()
	g.waits = keep
	g.mu.Unlock()
}

// retryBlocked resumes a group blocked by a blocker error when its wait ends; the streak stays, so the next wait grows.
// The fired wait is cleared first, so a retry that fails leaves a plain block and the stall is reported.
func (g *group) retryBlocked() {
	if !waitingBlock(g.st) || g.holding() {
		return
	}

	g.clearHold()

	streak := g.st.Counters.FaultStreak
	res := Next(g.st, Input{Kind: InContinue, Group: g.spec.ID, Time: g.now()}, g.r.limits)
	if res.Refused || res.Dup || res.Stale {
		g.after()
		return
	}

	res.State.Counters.FaultStreak = streak
	if err := g.save(res.State); err != nil {
		g.saveFailed(err)
		return
	}

	g.publish(res.Emit)
	g.after()
	g.perform(res.Orders)
}
