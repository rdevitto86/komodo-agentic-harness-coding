package engine

import (
	"time"
)

// armTimers arms the timers a paused group waits on; an unchanged pause keeps the timers it has.
func (g *group) armTimers() {
	s := g.st

	key := pauseKey{
		stage: s.Stage,
		until: s.Pause.Until,
		since: s.Pause.Since,
		cause: s.Pause.Cause,
	}
	if key == g.armed {
		return
	}

	for _, t := range g.timers {
		t.Stop()
	}

	g.timers = nil
	g.armed = key

	if s.Stage != StagePaused {
		return
	}

	now := g.now()
	if !s.Pause.Until.IsZero() {
		g.timers = append(
			g.timers,
			g.m.clk.AfterFunc(max(s.Pause.Until.Sub(now), 0), func() { g.timerDue("timer") }),
		)
	}

	if s.Pause.Cause == "provider" && g.r.limits.PauseMaxMin > 0 && !s.Pause.Since.IsZero() {
		at := s.Pause.Since.Add(time.Duration(g.r.limits.PauseMaxMin) * time.Minute)
		g.timers = append(
			g.timers,
			g.m.clk.AfterFunc(max(at.Sub(now), 0), func() { g.timerDue("pause_timeout") }),
		)
	}
}

// timerDue hands a fired pause timer to the group's loop if the pause it was armed for still
// stands.
func (g *group) timerDue(reason string) {
	g.mu.Lock()
	s := g.snap
	g.mu.Unlock()
	now := g.now()

	if s.Stage != StagePaused {
		return
	}

	if reason == "timer" && (s.Pause.Until.IsZero() || s.Pause.Until.After(now)) {
		return
	}

	g.enqueue(Input{Kind: InTimerDue, Time: now, Data: mustJSON(TimerDue{Reason: reason})})
}

// nextMidnight returns the next local midnight after t.
func nextMidnight(t time.Time) time.Time {
	y, m, d := t.Date()

	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())
}
