package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// intakeEvents registers the plan event types once for the package.
var intakeEvents sync.Once

// fakeIntake answers the next plan event with an intake run-start for the labels, as the Intake Handler does on success.
func (x *eng) fakeIntake(runID string, labels ...string) {
	x.t.Helper()
	intakeEvents.Do(intake.RegisterEvents)
	sub, err := x.bus.Subscribe("fake-intake", bus.TypePlan)
	if err != nil {
		x.t.Fatal(err)
	}
	go func() {
		defer sub.Close()
		for ev := range sub.Events {
			if _, ok := ev.Data.(*intake.PlanRequest); !ok {
				continue
			}
			_ = x.bus.Publish(Event{Type: bus.TypeRunStart, Run: bus.ID(runID), Data: &bus.RunStart{
				Target: "ep-1", Groups: labels, Source: "intake", Repo: "github.com/acme/shop",
				Root: x.root, CommonDir: filepath.Join(x.root, ".git"),
			}})
			return
		}
	}()
}

// planReq builds a plan request carrying a plan event.
func (x *eng) planReq(id, planRun string) Request {
	return Request{
		ID:   id,
		Verb: VerbPlan,
		Root: x.root,
		Event: &Event{
			Type: bus.TypePlan, Subtype: "request", Run: bus.ID(planRun),
			Data: &intake.PlanRequest{Goal: "g", Docs: []string{}, Repo: "github.com/acme/shop", Root: x.root},
		},
	}
}

func TestPlanStreamsTheRefusalOfTheRunItStarted(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1", "tg-2"}, {"tg-2", "tg-1"}})
	x.serve()
	x.fakeIntake("run-p", "tg-1", "tg-2")

	reps := x.ask(x.planReq("p1", "plan-1"))

	var start, refused *Event
	for _, rp := range reps {
		switch {
		case rp.Event == nil:
		case rp.Event.Type == bus.TypeRunStart:
			start = rp.Event
		case rp.Event.Subtype == "run-refused":
			refused = rp.Event
		}
	}
	if start == nil || refused == nil || refused.Run != "run-p" {
		t.Fatalf("replies %+v", reps)
	}
	if rr, ok := refused.Data.(*RunRefused); !ok || !strings.Contains(rr.Why, "dependency cycle") {
		t.Fatalf("refusal %+v", refused.Data)
	}
}

func TestPlanEndsWhenTheRunItStartedIsAccepted(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle)
	x.serve()
	x.fakeIntake("run-p", "tg-1")

	reps := x.ask(x.planReq("p1", "plan-1"))

	if last := reps[len(reps)-1]; last.Status != StatusOK || !last.Final {
		t.Fatalf("last reply %+v", last)
	}
	for _, rp := range reps {
		if rp.Event != nil && rp.Event.Subtype == "run-refused" {
			t.Fatalf("refused: %+v", rp.Event)
		}
	}
}
