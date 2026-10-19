package engine

import "time"

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

// holdFor holds the group's session starts back for wait and asks for them again when it ends; 0 holds nothing.
func (g *group) holdFor(wait time.Duration) {
	g.clearHold()
	if wait <= 0 {
		return
	}

	g.holdUntil = g.now().Add(wait)
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
}
