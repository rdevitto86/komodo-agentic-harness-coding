package task

import (
	"fmt"
	"strconv"
	"sync"
)

const (
	sameFailLimit = 3  // the same check failing with the same error this many times in a row ends the session
	checkRunLimit = 20 // checks one builder session may run
)

// checkCap is the inner cap on a try: it counts one builder session's check runs and ends the session on a firm failure.
type checkCap struct {
	mu     sync.Mutex
	runs   int
	checks map[string]*checkTrack
}

// checkTrack is what the cap remembers of one check.
type checkTrack struct {
	sig     string // exit code and output of the last failure
	streak  int    // failures in a row with that signature
	last    int    // 1 after a pass, -1 after a failure, 0 before any run
	flips   int    // changes between pass and fail
	flagged bool   // the check was already reported flaky
}

// capVerdict is what the cap decides after one check run.
type capVerdict struct {
	end    string // why the session ends; empty when it goes on
	flaky  bool   // the check was found flaky by this run
	failed bool   // the run failed
}

// record counts one run of a check and returns whether the session must end. A pass clears the streak, and a check that
// alternates pass and fail is flaky and never builds a streak.
func (c *checkCap) record(check string, exit int, out string) capVerdict {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.checks == nil {
		c.checks = map[string]*checkTrack{}
	}

	t := c.checks[check]
	if t == nil {
		t = &checkTrack{}
		c.checks[check] = t
	}

	c.runs++

	v := capVerdict{failed: exit != 0}
	now := 1
	if exit != 0 {
		now = -1
	}

	if t.last != 0 && t.last != now {
		t.flips++
	}

	t.last = now
	if t.flips >= 2 && !t.flagged {
		t.flagged, v.flaky = true, true
	}

	if exit == 0 {
		t.streak, t.sig = 0, ""
	} else if sig := strconv.Itoa(exit) + "\n" + out; sig == t.sig {
		t.streak++
	} else {
		t.sig, t.streak = sig, 1
	}

	switch {
	case t.streak >= sameFailLimit:
		v.end = fmt.Sprintf("the check %s failed %d times in a row with the same error", check, t.streak)
	case c.runs >= checkRunLimit:
		v.end = fmt.Sprintf("the session ran checks %d times", c.runs)
	}

	return v
}

// takeLastCheck returns the output that ended a builder session by the inner cap and clears it.
func (m *Manager) takeLastCheck() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.lastCheck
	m.lastCheck = ""

	return s
}
