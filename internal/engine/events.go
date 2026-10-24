package engine

import (
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// StalledGroup is one group that cannot go on.
type StalledGroup struct {
	Group  string `json:"group"`  // the group label
	Reason string `json:"reason"` // the block reason, or dependency for a group that waits on a blocked one
	On     string `json:"on,omitempty"`
}

// Stalled is the data of run-failure / stalled.
type Stalled struct {
	Groups []StalledGroup `json:"groups"`
}

// EventKey binds Stalled to run-failure / stalled.
func (Stalled) EventKey() bus.Key { return bus.Key{Type: bus.TypeRunFailure, Subtype: "stalled"} }

// RunRefused is the data of error / run-refused.
type RunRefused struct {
	Target  string `json:"target"`
	Why     string `json:"why"`
	Request string `json:"request,omitempty"`
}

// EventKey binds RunRefused to error / run-refused.
func (RunRefused) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "run-refused"} }

// ResumeLost is the data of run-status / session.resume-lost.
type ResumeLost struct {
	bus.GroupFacts
	Agent string `json:"agent"`
	Error string `json:"error,omitempty"` // why the saved session could not be resumed
}

var registerOnce sync.Once

// EventKey binds ResumeLost to run-status / session.resume-lost.
func (ResumeLost) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "session.resume-lost"}
}

// ErrorRecorded is the data of run-status / error.recorded: the error handler classified one error of a group.
type ErrorRecorded struct {
	Reason string `json:"reason"`           // the error type
	Tier   string `json:"tier"`             // blocker, fault, minor, work or empty for none
	WaitS  int    `json:"wait_s,omitempty"` // seconds the group waits before its next session start; 0 for none
}

// EventKey binds ErrorRecorded to run-status / error.recorded.
func (ErrorRecorded) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "error.recorded"}
}

// StartsPaused is the data of run-failure / starts.paused: one error type hit several groups, so no new session starts.
type StartsPaused struct {
	Reason string   `json:"reason"`           // the error type that hit the groups
	Groups []string `json:"groups"`           // the groups that hit it
	WaitS  int      `json:"wait_s,omitempty"` // seconds until starts may go on; 0 when only a person ends the pause
}

// EventKey binds StartsPaused to run-failure / starts.paused.
func (StartsPaused) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunFailure, Subtype: "starts.paused"}
}

// StartsResumed is the data of run-status / starts.resumed: held groups may start sessions again.
type StartsResumed struct {
	Reason string `json:"reason,omitempty"` // the error type the pause was for
	By     string `json:"by"`               // wait when a wait ended, person when a person ran komodo run
}

// EventKey binds StartsResumed to run-status / starts.resumed.
func (StartsResumed) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "starts.resumed"}
}

// EngineFailed is the data of run-failure / engine.failed: the engine itself failed, so no new session starts.
type EngineFailed struct {
	Where  string `json:"where"`  // the part that failed: a goroutine, the state store or the event bus
	Detail string `json:"detail"` // what went wrong
}

// EventKey binds EngineFailed to run-failure / engine.failed.
func (EngineFailed) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunFailure, Subtype: "engine.failed"}
}

// BreakerOpened is the data of run-failure / breaker.opened; no engine publishes it now, and it stays so old logs read.
type BreakerOpened struct {
	Reason string   `json:"reason"` // the reason that tripped the old engine-wide breaker
	Groups []string `json:"groups"` // the groups that hit it
}

// EventKey binds BreakerOpened to run-failure / breaker.opened.
func (BreakerOpened) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunFailure, Subtype: "breaker.opened"}
}

// BreakerClosed is the data of run-status / breaker.closed; no engine publishes it now, and it stays so old logs read.
type BreakerClosed struct {
	Reason string `json:"reason"` // the reason the old breaker had opened for
}

// EventKey binds BreakerClosed to run-status / breaker.closed.
func (BreakerClosed) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "breaker.closed"}
}

// RegisterEvents binds the engine's event subtypes to their data structs; main calls it before the first publish.
func RegisterEvents() {
	registerOnce.Do(func() {
		bus.Register(Stalled{}.EventKey(), func() bus.Payload { return &Stalled{} })
		bus.Register(RunRefused{}.EventKey(), func() bus.Payload { return &RunRefused{} })
		bus.Register(ResumeLost{}.EventKey(), func() bus.Payload { return &ResumeLost{} })
		bus.Register(ErrorRecorded{}.EventKey(), func() bus.Payload { return &ErrorRecorded{} })
		bus.Register(StartsPaused{}.EventKey(), func() bus.Payload { return &StartsPaused{} })
		bus.Register(StartsResumed{}.EventKey(), func() bus.Payload { return &StartsResumed{} })
		bus.Register(EngineFailed{}.EventKey(), func() bus.Payload { return &EngineFailed{} })
		bus.Register(BreakerOpened{}.EventKey(), func() bus.Payload { return &BreakerOpened{} })
		bus.Register(BreakerClosed{}.EventKey(), func() bus.Payload { return &BreakerClosed{} })
	})
}
