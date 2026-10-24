package engine

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

func TestBulkApproveReplyNamesTheRiskyGroups(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}, {"tg-2", "tg-1"}, {"tg-3"}}, func(r *rig) {
		r.d.Cleaner = &fakeCleaner{}
		r.tasks.script = riskyScript
	})
	x.serve()
	x.ask(x.runReq("q1", "run-1", "tg-1", "tg-2", "tg-3"))
	x.waitHeld("tg-1", StageBlocked)

	reps := x.ask(Request{ID: "a1", Verb: VerbRun, Target: "ep-1", Flags: Flags{Approve: true},
		Event: &Event{
			Type: bus.TypeRunContinue,
			Time: t0,
			Data: &bus.RunContinue{Target: "ep-1", Answer: "approve", Request: "a1"},
		}})

	last := reps[len(reps)-1]
	if last.Status != StatusOK {
		t.Fatalf("reply %+v", last)
	}

	var d struct {
		Risky []string `json:"risky"`
	}
	if err := json.Unmarshal(last.Data, &d); err != nil || !slices.Equal(d.Risky, []string{"tg-1"}) {
		t.Fatalf("reply data %s names %v, %v", last.Data, d.Risky, err)
	}
}
