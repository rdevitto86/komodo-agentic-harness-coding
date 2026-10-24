package engine

import (
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// intakeEvents registers the plan event types once for the package.
var intakeEvents sync.Once

// fakeIntake answers the next plan event with the events given, all on the plan request's run.
func (x *eng) fakeIntake(events ...Event) {
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
			for _, out := range events {
				out.Run = ev.Run
				_ = x.bus.Publish(out)
			}
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

func TestPlanEndsWithTheErrorOfThePlanningRun(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	x.fakeIntake(Event{Type: bus.TypeError, Subtype: "plan-blocked", Data: &intake.PlanBlocked{}})

	reps := x.ask(x.planReq("p1", "plan-1"))

	var blocked bool
	for _, rp := range reps {
		blocked = blocked || rp.Event != nil && rp.Event.Subtype == "plan-blocked"
	}
	if last := reps[len(reps)-1]; last.Status != StatusOK || !last.Final || !blocked {
		t.Fatalf("replies %+v", reps)
	}
}

func TestPlanStartsNoRunAndIgnoresAnotherRunsEvents(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	other := Event{Type: bus.TypeError, Subtype: "plan-blocked", Run: "other-run", Data: &intake.PlanBlocked{}}
	_ = x.bus.Publish(other)
	x.fakeIntake(Event{Type: bus.TypeError, Subtype: "plan-timeout", Data: &intake.PlanTimeout{}})

	reps := x.ask(x.planReq("p1", "plan-1"))

	for _, rp := range reps {
		if rp.Event != nil && (rp.Event.Type == bus.TypeRunStart || rp.Event.Run == "other-run") {
			t.Fatalf("stray event %+v", rp.Event)
		}
	}
	if last := reps[len(reps)-1]; last.Status != StatusOK || x.m.Live() != 0 {
		t.Fatalf("last reply %+v with %d live runs", last, x.m.Live())
	}
}
