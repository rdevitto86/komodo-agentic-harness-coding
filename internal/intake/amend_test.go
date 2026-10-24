package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// seeded plans twoGroupDraft in a rig whose script then plays steps, and returns the rig and the stored blueprint.
func seeded(t *testing.T, steps ...any) (*rig, Blueprint) {
	t.Helper()

	r := newRig(t, append([]any{twoGroupDraft()}, steps...)...)

	res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "first"})
	if err != nil {
		t.Fatal(err)
	}

	b, err := LoadBlueprint(r.home, "repo-1", res.Plan)
	if err != nil {
		t.Fatal(err)
	}

	return r, b
}

// amendDraft renders the stored plan with edit applied to its groups, as the planner's draft of an amendment.
func amendDraft(stored Blueprint, edit func([]Group) []Group) PlanDraft {
	b := stored
	b.Groups = edit(slices.Clone(stored.Groups))

	return draftOfPlan(b)
}

// withTask gives group name a second task.
func withTask(groups []Group, name string) []Group {
	for i, g := range groups {
		if g.Name == name {
			g.Tasks = append(slices.Clone(g.Tasks), Task{
				Name: "tsk-two", Files: g.Files, DoneWhen: "go vet ./...", EstimateLines: 10,
			})
			groups[i] = g
		}
	}

	return groups
}

func TestAmend(t *testing.T) {
	t.Parallel()

	t.Run("a shape change that edits a running group returns LockedError and leaves the folder", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })}
		r.lock.runs[stored.Groups[0].ID] = "run-7"
		before, _ := os.ReadFile(filepath.Join(r.plans(), stored.Plan, "plan.md"))

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "split", Plan: stored.Plan})

		var locked LockedError
		if !errors.As(err, &locked) || locked.Run != "run-7" {
			t.Fatalf("err: %v", err)
		}

		after, _ := os.ReadFile(filepath.Join(r.plans(), stored.Plan, "plan.md"))
		if string(before) != string(after) || !slices.Equal(r.entries(), []string{stored.Plan}) {
			t.Fatalf("folder changed: %v", r.entries())
		}
	})

	t.Run("a shape change that drops a running group returns LockedError", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return g[:1] })}
		r.lock.runs[stored.Groups[1].ID] = "run-9"

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "drop", Plan: stored.Plan})

		var locked LockedError
		if !errors.As(err, &locked) || locked.Run != "run-9" {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a shape change that adds a group leaves running groups and keeps stored ids", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group {
			return append(g, bpGroup("tg-3", "tg-2"))
		})}
		r.lock.runs[stored.Groups[0].ID] = "run-7"

		res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "add", Plan: stored.Plan})
		if err != nil {
			t.Fatal(err)
		}

		got, err := LoadBlueprint(r.home, "repo-1", stored.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan != stored.Plan || !slices.Equal(res.Groups, []string{"tg-1", "tg-2", "tg-3"}) ||
			len(got.Groups) != 3 || got.Groups[0].ID != stored.Groups[0].ID || got.Groups[2].ID == "" ||
			!slices.Equal(r.entries(), []string{stored.Plan}) {
			t.Fatalf("result %+v entries %v groups %+v", res, r.entries(), got.Groups)
		}
		if plan := r.ctx.req.Slots[contextmgr.Plan]; !strings.Contains(plan, "tg-2") {
			t.Fatalf("plan slot: %q", plan)
		}
	})

	t.Run("a group rewrite of a running group never returns LockedError and publishes written", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })}
		r.lock.runs[stored.Groups[0].ID] = "run-7"

		out, plan := runRig(t, r)
		plan(PlanRequest{Goal: "add a task", Plan: stored.Plan, Group: "tg-1"}, "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57")

		e := next(t, out)
		w, ok := e.Data.(*PlanWritten)
		if e.Type != bus.TypePlan || e.Subtype != "written" || !ok || w.Plan != stored.Plan {
			t.Fatalf("event: %+v", e)
		}

		got, err := LoadBlueprint(r.home, "repo-1", stored.Plan)
		if err != nil || len(got.Groups[0].Tasks) != 2 || got.Groups[0].ID != stored.Groups[0].ID {
			t.Fatalf("blueprint %+v err %v", got.Groups, err)
		}
		if goal := r.ctx.req.Slots[contextmgr.Goal]; !strings.Contains(goal, "tg-1") || !strings.Contains(goal, "add a task") {
			t.Fatalf("goal slot: %q", goal)
		}
	})

	t.Run("a group rewrite that changes another group is a failed draft", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		bad := amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-2") })
		r.sess.steps = []any{bad, bad, bad}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"})

		var blocked PlanBlockedError
		if !errors.As(err, &blocked) || !strings.Contains(findingText(blocked.Findings), "tg-2") {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a group rewrite that adds a group is a failed draft", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		add := amendDraft(stored, func(g []Group) []Group { return append(g, bpGroup("tg-3")) })
		r.sess.steps = []any{add, add, add}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"})

		var blocked PlanBlockedError
		if !errors.As(err, &blocked) || !strings.Contains(findingText(blocked.Findings), "tg-3") {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a group rewrite that drops a group is a failed draft", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		drop := amendDraft(stored, func(g []Group) []Group { return g[:1] })
		r.sess.steps = []any{drop, drop, drop}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"})

		var blocked PlanBlockedError
		if !errors.As(err, &blocked) || !strings.Contains(findingText(blocked.Findings), "tg-2") {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("an amendment is linted against the stored plan and a failed draft is sent back", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		skip := amendDraft(stored, func(g []Group) []Group { return append(g, bpGroup("tg-5")) })
		good := amendDraft(stored, func(g []Group) []Group { return append(g, bpGroup("tg-3")) })
		r.sess.steps = []any{skip, good}

		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "add", Plan: stored.Plan}); err != nil {
			t.Fatal(err)
		}
		if len(r.sess.texts) != 3 || !strings.Contains(r.sess.texts[2], "tg-3") {
			t.Fatalf("texts: %q", r.sess.texts)
		}
	})

	t.Run("a lock reader that fails stops a shape change", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })}
		r.h.d.Locks = fakeLocks{err: errors.New("state gone")}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "split", Plan: stored.Plan})

		var locked LockedError
		if err == nil || errors.As(err, &locked) || !strings.Contains(err.Error(), "state gone") {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a shape change with no lock reader fails closed", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })}
		r.h.d.Locks = nil

		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "split", Plan: stored.Plan}); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("a rewrite sends the finished tasks to the planner as fixed", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })}
		r.lock.finished[stored.Groups[0].ID] = []string{"tsk-one"}

		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "more", Plan: stored.Plan, Group: "tg-1"}); err != nil {
			t.Fatal(err)
		}
		if goal := r.ctx.req.Slots[contextmgr.Goal]; !strings.Contains(goal, "tsk-one") || !strings.Contains(goal, "finished") {
			t.Fatalf("goal slot: %q", goal)
		}
	})

	t.Run("a rewrite draft that changes a finished task is a failed draft sent back", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		bad := amendDraft(stored, func(g []Group) []Group {
			g[0].Tasks = slices.Clone(g[0].Tasks)
			g[0].Tasks[0].DoneWhen = "go vet ./..."

			return g
		})
		good := amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })
		r.sess.steps = []any{bad, good}
		r.lock.finished[stored.Groups[0].ID] = []string{"tsk-one"}

		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"}); err != nil {
			t.Fatal(err)
		}
		if len(r.sess.texts) != 3 || !strings.Contains(r.sess.texts[2], "grammar") || !strings.Contains(r.sess.texts[2], "tsk-one") {
			t.Fatalf("texts: %q", r.sess.texts)
		}
	})

	t.Run("a rewrite draft that drops a finished task is refused after three drafts", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		drop := amendDraft(stored, func(g []Group) []Group {
			g[0].Tasks = []Task{{
				Name: "tsk-new", Files: g[0].Files, DoneWhen: "go vet ./...", EstimateLines: 10,
				Acceptance: g[0].Tasks[0].Acceptance,
			}}

			return g
		})
		r.sess.steps = []any{drop, drop, drop}
		r.lock.finished[stored.Groups[0].ID] = []string{"tsk-one"}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"})

		var blocked PlanBlockedError
		if !errors.As(err, &blocked) || !strings.Contains(findingText(blocked.Findings), "tsk-one") {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a rewrite fails when the lock reader cannot read the finished tasks", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.h.d.Locks = fakeLocks{err: errors.New("state gone")}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"})
		if err == nil || !strings.Contains(err.Error(), "state gone") {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a rewrite with no lock reader keeps no task fixed", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return withTask(g, "tg-1") })}
		r.h.d.Locks = nil

		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan, Group: "tg-1"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a request names a stored plan and a stored group", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)

		for name, req := range map[string]PlanRequest{
			"unknown plan":   {Goal: "g", Plan: "plan-ffffff"},
			"group, no plan": {Goal: "g", Group: "tg-1"},
			"unknown group":  {Goal: "g", Plan: stored.Plan, Group: "tg-9"},
			"no prompt":      {Plan: stored.Plan},
			"unsafe plan id": {Goal: "g", Plan: "../x"},
			"documents only": {Docs: []string{"docs/hld.md"}, Plan: stored.Plan, Group: "tg-1"},
		} {
			before := r.sess.started
			if _, err := r.h.Plan(context.Background(), req); err == nil || r.sess.started != before {
				t.Fatalf("%s: err %v sessions %d", name, err, r.sess.started-before)
			}
		}
	})

	t.Run("a failed move into place restores the old folder", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return append(g, bpGroup("tg-3")) })}

		calls := 0
		r.h.rename = func(from, to string) error {
			calls++
			if calls == 2 {
				return errors.New("disk gone")
			}

			return osRename(from, to)
		}

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "add", Plan: stored.Plan})

		var w writeError
		if !errors.As(err, &w) || !slices.Equal(r.entries(), []string{stored.Plan}) {
			t.Fatalf("err %v entries %v", err, r.entries())
		}

		got, lerr := LoadBlueprint(r.home, "repo-1", stored.Plan)
		if lerr != nil || len(got.Groups) != 2 {
			t.Fatalf("blueprint %+v err %v", got.Groups, lerr)
		}
	})

	t.Run("a failed move aside leaves the old folder", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return append(g, bpGroup("tg-3")) })}
		r.h.rename = func(string, string) error { return errors.New("disk gone") }

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "add", Plan: stored.Plan})

		var w writeError
		if !errors.As(err, &w) || !slices.Equal(r.entries(), []string{stored.Plan}) {
			t.Fatalf("err %v entries %v", err, r.entries())
		}
	})

	t.Run("fail-first checks only the groups the amendment adds or changes", func(t *testing.T) {
		t.Parallel()

		r, stored := seeded(t)
		added := bpGroup("tg-3", "tg-2")
		added.Tasks[0].DoneWhen = "make t3"
		r.sess.steps = []any{amendDraft(stored, func(g []Group) []Group { return append(g, added) })}

		base := r.withBase(nil)

		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "x", Plan: stored.Plan})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(base.ran, []string{"make t3"}) {
			t.Fatalf("commands run: %v", base.ran)
		}
	})
}
