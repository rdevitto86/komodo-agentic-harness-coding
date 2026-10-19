package cli

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
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

func TestBlockedFixNamesAnAnswerOnlyForScopeWallAndRetries(t *testing.T) {
	cases := map[string]string{
		"retries":       "komodo run tg-1.1 --override in a terminal, before the window ends",
		"state_invalid": "komodo run tg-1.1",
		"needs_scope":   "komodo run tg-1.1 --approve or --deny",
		"wall":          "komodo run tg-1.1 --approve or --deny",
	}
	for reason, want := range cases {
		res := blockedResult(StatusData{Blocked: []BlockedRow{{Group: "tg-1.1", Reason: reason}}})
		facts, ok := res.Facts.(format.NeedsFacts)
		if !ok || facts.Fix != want {
			t.Errorf("%s: fix %+v, want %q", reason, res.Facts, want)
		}
	}
}
