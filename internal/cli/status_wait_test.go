package cli

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

func TestStatusWaitKeepsTheBlockedRowsOfTheFinalFrame(t *testing.T) {
	frames := []engine.Reply{
		{Status: engine.StatusOK, Data: []byte(`{"runs":[{"run_id":"r1","target":"tg-1.1"}],"blocked":[]}`)},
		{
			Status: engine.StatusOK,
			Final:  true,
			Data: []byte(`{"runs":[{"run_id":"r1","target":"tg-1.1"}],` +
				`"blocked":[{"group":"tg-1.1","run_id":"r1","reason":"needs_scope","question":"may it edit outside.go?"}],` +
				`"waited":"blocked"}`),
		},
	}

	sd := statusFrames(frames, true)
	if sd.Waited != "blocked" || len(sd.Blocked) != 1 || sd.Blocked[0].Question == "" {
		t.Fatalf("status %+v, want waited blocked with the question of the blocked group", sd)
	}
}
