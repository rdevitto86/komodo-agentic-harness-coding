package task

import (
	"context"
	"testing"
)

func TestMessageIdentifiersDifferBetweenTaskManagerStarts(t *testing.T) {
	seen := map[string]bool{}
	for range 2 {
		out := make(chan Message, 4)
		cr := newChanRuntime(context.Background(), out)
		if err := cr.ReportState(context.Background(), StateReport{}); err != nil {
			t.Fatal(err)
		}

		id := (<-out).ID
		if seen[id] {
			t.Fatalf("message id %q repeats across starts; the engine would drop it as a duplicate request", id)
		}
		seen[id] = true
	}

	a, b := &Manager{tag: newTag()}, &Manager{tag: newTag()}
	if a.nextID("esc") == b.nextID("esc") {
		t.Fatal("escalation ids repeat across Task Managers")
	}
}
