package metrics

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// recorded is the body of the engine's error.recorded event, which the metrics read by its JSON shape.
type recorded struct {
	Reason string `json:"reason"`
	Tier   string `json:"tier"`
}

func (recorded) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "error.recorded"}
}

func TestARunCountsItsErrorsByType(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2"))
	f.apply(ev(1, bus.TypeRunStatus, "error.recorded", "r", "g1", &recorded{Reason: "crash", Tier: "blocker"}))
	f.apply(ev(2, bus.TypeRunStatus, "error.recorded", "r", "g2", &recorded{Reason: "crash", Tier: "blocker"}))
	f.apply(ev(3, bus.TypeRunStatus, "error.recorded", "r", "g1", &recorded{Reason: "timeout", Tier: "work"}))

	r, _ := f.run("r")
	if r.Errors["crash"] != 2 || r.Errors["timeout"] != 1 || len(r.Errors) != 2 {
		t.Fatalf("errors by type = %v", r.Errors)
	}
}
