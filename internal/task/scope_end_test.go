package task

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestScopeRequestEndsTheBuilderSessionWithReasonNeedsScope(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", sayResult(needsScope("lib/y.go")))
	if err := r.run(); !errors.Is(err, ErrHeld) {
		t.Fatalf("Run returned %v", err)
	}

	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()
	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" {
			continue
		}
		var d struct {
			Reason string   `json:"reason"`
			Paths  []string `json:"scope_paths"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "needs_scope" || len(d.Paths) != 1 || d.Paths[0] != "lib/y.go" {
			t.Fatalf("session.end carries %s, want reason needs_scope and the path asked for", f.Data)
		}
		return
	}
	t.Fatal("no session.end fact")
}
