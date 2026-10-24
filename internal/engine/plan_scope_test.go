package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// startPlan publishes a run-start for a plan and waits for the Runtime Manager to settle it.
func (r *rig) startPlan(plan, runID string, labels ...string) error {
	r.t.Helper()
	req := "req-" + runID
	ch := r.m.Expect(req)
	rs := &bus.RunStart{
		Target: plan, Groups: labels, Source: "cli", Repo: "github.com/acme/shop", Root: r.root,
		CommonDir: filepath.Join(r.root, ".git"), Request: bus.ID(req),
	}
	if err := r.bus.Publish(Event{Type: bus.TypeRunStart, Run: bus.ID(runID), Data: rs}); err != nil {
		r.t.Fatal(err)
	}
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		r.t.Fatal("the run start was not settled")
	}
	return nil
}

// twoPlans gives the plans plan-a and plan-b one group each, both labelled tg-1 with their own ids.
func twoPlans(r *rig) {
	one := func(id string) map[string]GroupSpec {
		return map[string]GroupSpec{"tg-1": {
			ID: id, Label: "tg-1", Epic: "ep-1", Tasks: []string{"t-" + id}, Files: []string{id + "/**"},
		}}
	}
	r.plans.plans = map[string]map[string]GroupSpec{"plan-a": one("g-a"), "plan-b": one("g-b")}
}

func TestRunStartWholePlan(t *testing.T) {
	t.Parallel()

	t.Run("an empty group list runs only the groups not Done", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, [][]string{{"tg-1"}, {"tg-2", "tg-1"}, {"tg-3"}}, onlyIdle)
		r.store.put("g-tg-1", State{Stage: StageDone, Outcome: "complete", Run: "run-0"}, 1)

		if err := r.startPlan("ep-1", "run-1"); err != nil {
			t.Fatal(err)
		}
		r.waitStage("tg-2", StageBuild)
		r.waitStage("tg-3", StageBuild)

		if n := r.tasks.startedCount("g-tg-1"); n != 0 {
			t.Fatalf("the Done group started %d times", n)
		}
		if r.plans.asked[0] != nil && len(r.plans.asked[0]) != 0 {
			t.Fatalf("the plan was asked for labels %v", r.plans.asked[0])
		}
	})

	t.Run("a plan with no groups is refused as an unknown target", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
		r.plans.plans = map[string]map[string]GroupSpec{"plan-empty": {}}

		var ce *codeError
		if err := r.startPlan("plan-empty", "run-1"); !errors.As(err, &ce) || ce.code != CodeUnknownTarget {
			t.Fatalf("got %v", err)
		}
	})
}

func TestTwoPlansThatBothHoldTg1(t *testing.T) {
	t.Parallel()

	start := func(t *testing.T) *rig {
		t.Helper()
		r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
		twoPlans(r)
		if err := r.startPlan("plan-a", "run-a", "tg-1"); err != nil {
			t.Fatal(err)
		}
		if err := r.startPlan("plan-b", "run-b", "tg-1"); err != nil {
			t.Fatal(err)
		}
		until(t, "both groups to build", func() bool {
			return r.store.get("g-a").Stage == StageBuild && r.store.get("g-b").Stage == StageBuild
		})
		return r
	}

	t.Run("stop of one plan's group leaves the other plan running", func(t *testing.T) {
		t.Parallel()
		r := start(t)

		if got := r.m.RunOf("plan-a tg-1"); got != "run-a" {
			t.Fatalf("run of plan-a tg-1 is %q", got)
		}
		if err := r.m.stopTarget(context.Background(), "plan-a tg-1"); err != nil {
			t.Fatal(err)
		}
		if gs := r.m.byPlan("plan-b", "tg-1"); len(gs) != 1 || r.m.RunOf("plan-b tg-1") != "run-b" {
			t.Fatalf("plan-b lost its group: %d live", len(gs))
		}
		if gs := r.m.byPlan("plan-a", "tg-1"); len(gs) != 0 {
			t.Fatalf("plan-a still has %d live groups", len(gs))
		}
	})

	t.Run("abandon drops only the named plan's group", func(t *testing.T) {
		t.Parallel()
		r := start(t)

		if _, err := r.m.Abandon(context.Background(), "plan-b", "tg-1"); err != nil {
			t.Fatal(err)
		}
		if st := r.store.get("g-b"); st.Outcome != "abandoned" {
			t.Fatalf("plan-b group outcome %q", st.Outcome)
		}
		if st := r.store.get("g-a"); st.Stage != StageBuild {
			t.Fatalf("plan-a group left %s", st.Stage)
		}
	})

	t.Run("an answer reaches only the named plan's group", func(t *testing.T) {
		t.Parallel()
		r := start(t)

		var ce *codeError
		err := r.m.Answer("plan-a", []string{"tg-1"}, true, "a1")
		if !errors.As(err, &ce) || ce.code != CodeNotBlocked {
			t.Fatalf("got %v", err)
		}
		err = r.m.Answer("plan-c", []string{"tg-1"}, true, "a2")
		if !errors.As(err, &ce) || ce.code != CodeUnknownTarget {
			t.Fatalf("an unknown plan got %v", err)
		}
	})

	t.Run("a bare label names nothing", func(t *testing.T) {
		t.Parallel()
		r := start(t)

		if got := r.m.RunOf("tg-1"); got != "" {
			t.Fatalf("a bare label found run %q", got)
		}
		if err := r.m.stopTarget(context.Background(), "tg-1"); err == nil {
			t.Fatal("a bare label stopped a run")
		}
	})
}
