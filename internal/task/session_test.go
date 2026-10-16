package task

import (
	"testing"
	"time"
)

// t0 is a fixed start for the clock tests.
var t0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

// lim is a one-hour wall and a ten-minute idle limit.
var lim = SessionLimits{Wall: time.Hour, Idle: 10 * time.Minute}

func TestTickWarnsAtEightyPercentOnce(t *testing.T) {
	c := &Clocks{Started: t0, LastLine: t0.Add(47 * time.Minute)}
	if got := c.Tick(t0.Add(47*time.Minute+59*time.Second), lim); got != TickNone {
		t.Fatalf("before 80%%: got %v", got)
	}
	c.LastLine = t0.Add(48 * time.Minute)
	if got := c.Tick(t0.Add(48*time.Minute), lim); got != TickWarn {
		t.Fatalf("at 80%%: got %v, want TickWarn", got)
	}
	c.Warned = true
	if got := c.Tick(t0.Add(49*time.Minute), lim); got != TickNone {
		t.Fatalf("after the warning: got %v", got)
	}
}

func TestTickStopsOnTheWallLimitAndSkipsPausedTime(t *testing.T) {
	c := &Clocks{Started: t0, Warned: true, LastLine: t0.Add(59 * time.Minute)}
	if got := c.Tick(t0.Add(time.Hour), lim); got != TickStopTimeout {
		t.Fatalf("at the wall limit: got %v", got)
	}
	c.PausedFor = 30 * time.Minute
	if got := c.Tick(t0.Add(time.Hour), lim); got != TickNone {
		t.Fatalf("with 30 minutes paused: got %v", got)
	}
}

func TestTickNudgesThenStopsIdleAfterTheGrace(t *testing.T) {
	c := &Clocks{Started: t0, Warned: true, LastLine: t0}
	if got := c.Tick(t0.Add(9*time.Minute+59*time.Second), lim); got != TickNone {
		t.Fatalf("under the idle limit: got %v", got)
	}
	if got := c.Tick(t0.Add(10*time.Minute), lim); got != TickNudge {
		t.Fatalf("at the idle limit: got %v, want TickNudge", got)
	}
	c.NudgedAt = t0.Add(10 * time.Minute)
	if got := c.Tick(t0.Add(14*time.Minute+59*time.Second), lim); got != TickNone {
		t.Fatalf("inside the grace: got %v", got)
	}
	if got := c.Tick(t0.Add(15*time.Minute), lim); got != TickStopIdle {
		t.Fatalf("after the grace: got %v, want TickStopIdle", got)
	}
}

func TestTickRecoveredSessionGetsNoSecondNudge(t *testing.T) {
	c := &Clocks{Started: t0, Warned: true, NudgedAt: t0.Add(10 * time.Minute), LastLine: t0.Add(11 * time.Minute)}
	if got := c.Tick(t0.Add(20*time.Minute+59*time.Second), lim); got != TickNone {
		t.Fatalf("recovered and quiet under the limit: got %v", got)
	}
	if got := c.Tick(t0.Add(21*time.Minute), lim); got != TickStopIdle {
		t.Fatalf("recovered then silent again: got %v, want TickStopIdle with no second nudge", got)
	}
}

func TestTickCountsRunningToolsAndRetryDelaysAsAlive(t *testing.T) {
	c := &Clocks{Started: t0, Warned: true, LastLine: t0, ToolsRunning: 1}
	if got := c.Tick(t0.Add(30*time.Minute), lim); got != TickNone {
		t.Fatalf("a tool call in flight: got %v", got)
	}
	c.ToolsRunning = 0
	c.RetryUntil = t0.Add(31 * time.Minute)
	if got := c.Tick(t0.Add(30*time.Minute), lim); got != TickNone {
		t.Fatalf("inside a retry delay: got %v", got)
	}
	if got := c.Tick(t0.Add(31*time.Minute+10*time.Minute), lim); got != TickNudge {
		t.Fatalf("silent after the delay: got %v", got)
	}
}
