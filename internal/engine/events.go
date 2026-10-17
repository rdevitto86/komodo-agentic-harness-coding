package engine

import (
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// StalledGroup is one group that cannot go on.
type StalledGroup struct {
	// the group label
	Group string `json:"group"`
	// the block reason, or dependency for a group that waits on a blocked one
	Reason string `json:"reason"`
	On     string `json:"on,omitempty"`
}

// Stalled is the data of run-failure / stalled.
type Stalled struct {
	Groups []StalledGroup `json:"groups"`
}

// RunRefused is the data of error / run-refused.
type RunRefused struct {
	Target  string `json:"target"`
	Why     string `json:"why"`
	Request string `json:"request,omitempty"`
}

// ResumeLost is the data of run-status / session.resume-lost.
type ResumeLost struct {
	bus.GroupFacts
	Agent string `json:"agent"`
}

var registerOnce sync.Once

// EventKey binds Stalled to run-failure / stalled.
func (Stalled) EventKey() bus.Key { return bus.Key{Type: bus.TypeRunFailure, Subtype: "stalled"} }

// EventKey binds RunRefused to error / run-refused.
func (RunRefused) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "run-refused"} }

// EventKey binds ResumeLost to run-status / session.resume-lost.
func (ResumeLost) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "session.resume-lost"}
}

// RegisterEvents binds the engine's event subtypes to their data structs; main calls it before the
// first publish.
func RegisterEvents() {
	registerOnce.Do(func() {
		bus.Register(Stalled{}.EventKey(), func() bus.Payload { return &Stalled{} })
		bus.Register(RunRefused{}.EventKey(), func() bus.Payload { return &RunRefused{} })
		bus.Register(ResumeLost{}.EventKey(), func() bus.Payload { return &ResumeLost{} })
	})
}
