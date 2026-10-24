package engine

import (
	"encoding/json"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

func TestPlanEndsWithThePlanIDAndPath(t *testing.T) {
	t.Run("written", func(t *testing.T) {
		x := newEng(t, [][]string{{"tg-1"}})
		x.serve()
		x.fakeIntake(Event{
			Type: bus.TypePlan, Subtype: "written",
			Data: &intake.PlanWritten{Plan: "plan-7f3a9c", Path: "/h/plans/shop/plan-7f3a9c/plan.md", Groups: []string{"tg-1"}},
		})

		reps := x.ask(x.planReq("p1", "plan-1"))

		last := reps[len(reps)-1]
		var got struct{ Plan, Path string }
		if err := json.Unmarshal(last.Data, &got); err != nil || last.Status != StatusOK {
			t.Fatalf("final %+v: %v", last, err)
		}
		if got.Plan != "plan-7f3a9c" || got.Path != "/h/plans/shop/plan-7f3a9c/plan.md" {
			t.Fatalf("final data = %s", last.Data)
		}
	})

	t.Run("error", func(t *testing.T) {
		x := newEng(t, [][]string{{"tg-1"}})
		x.serve()
		x.fakeIntake(Event{Type: bus.TypeError, Subtype: "plan-timeout", Data: &intake.PlanTimeout{}})

		reps := x.ask(x.planReq("p1", "plan-2"))

		if last := reps[len(reps)-1]; last.Status != StatusOK || len(last.Data) != 0 {
			t.Fatalf("final %+v", last)
		}
	})
}
