package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cleanup"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

const (
	testRepo = "github.com/acme/shop"
	idOne    = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f01"
	idTwo    = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f02"
	idNone   = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f99"
	testRun  = "0198f3a2-6c1e-7b44-9d0a-3f5e1c2b7a10"
	testHash = "023c648e364af6cf885bc15be12621607595a7ef64533179971d9e8d0603cb2c"
)

// writePlan writes a two-group blueprint into the plan folder of testRepo under home.
func writePlan(t *testing.T, home, plan string) {
	t.Helper()

	bp := intake.Blueprint{
		Plan: plan, Repo: intake.RepoFolder(testRepo), Goal: "Ship it", Release: "V1",
		Groups: []intake.Group{
			{
				ID: idOne, Name: "tg-1", Goal: "First", Files: []string{"a.go"}, Priority: intake.PriorityHigh,
				Tasks: []intake.Task{{Name: "tsk-a", DoneWhen: "true"}},
			},
			{
				ID: idTwo, Name: "tg-2", Goal: "Second", Files: []string{"b.go"}, Priority: intake.PriorityNormal,
				Dependencies: []string{"tg-1"}, Tasks: []intake.Task{{Name: "tsk-b"}, {Name: "tsk-c"}},
			},
		},
	}
	if err := intake.WriteBlueprint(home, bp); err != nil {
		t.Fatal(err)
	}
}

func repoOf(string) (string, error) { return testRepo, nil }

func TestPlanSource(t *testing.T) {
	t.Parallel()

	t.Run("Specs lists the groups in plan order with dependency ids", func(t *testing.T) {
		t.Parallel()

		home := t.TempDir()
		writePlan(t, home, "plan-1")

		got, err := newPlanSource(home, repoOf).Specs(context.Background(), "/r", "plan-1", nil)
		if err != nil || len(got) != 2 {
			t.Fatalf("specs %+v, %v", got, err)
		}
		if got[1].Epic != "plan-1" || got[1].Order != 2 || !slices.Equal(got[1].Depends, []string{idOne}) ||
			!slices.Equal(got[1].Tasks, []string{"tsk-b", "tsk-c"}) || got[0].Priority != "high" {
			t.Fatalf("second spec %+v", got[1])
		}
	})

	t.Run("Specs filters by label and refuses an unknown one", func(t *testing.T) {
		t.Parallel()

		home := t.TempDir()
		writePlan(t, home, "plan-1")
		p := newPlanSource(home, repoOf)

		got, err := p.Specs(context.Background(), "/r", "plan-1", []string{"tg-2"})
		if err != nil || len(got) != 1 || got[0].Label != "tg-2" {
			t.Fatalf("specs %+v, %v", got, err)
		}
		if _, err := p.Specs(context.Background(), "/r", "plan-1", []string{"tg-9"}); err == nil {
			t.Fatal("an unknown label was accepted")
		}
		if _, err := p.Specs(context.Background(), "/r", "plan-none", nil); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a missing plan gave %v", err)
		}
	})

	t.Run("a repository lookup failure is returned", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("no repo")
		p := newPlanSource(t.TempDir(), func(string) (string, error) { return "", boom })

		if _, err := p.Specs(context.Background(), "/r", "plan-1", nil); !errors.Is(err, boom) {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("Facts names every group of the plan and survives the plan folder", func(t *testing.T) {
		t.Parallel()

		home := t.TempDir()
		writePlan(t, home, "plan-1")
		p := newPlanSource(home, repoOf)
		goals := []string{"tg-1: First", "tg-2: Second"}

		got, err := p.Facts(context.Background(), "/r", "plan-1")
		if err != nil || got.Goal != "Ship it" || got.Release != "V1" || !slices.Equal(got.GroupGoals, goals) {
			t.Fatalf("facts %+v, %v", got, err)
		}
		if err := os.RemoveAll(intake.PlanDir(home, intake.RepoFolder(testRepo), "plan-1")); err != nil {
			t.Fatal(err)
		}

		got, err = p.Facts(context.Background(), "/r", "plan-1")
		if err != nil || !slices.Equal(got.GroupGoals, goals) {
			t.Fatalf("facts after delete %+v, %v", got, err)
		}
		if _, err := p.Facts(context.Background(), "/r", "plan-other"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("facts of an unknown plan gave %v", err)
		}
	})
}

func TestBlueprintLabels(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writePlan(t, home, "plan-1")
	b := blueprintLabels{home: home, folder: intake.RepoFolder(testRepo)}

	got, err := b.Labels(context.Background(), "plan-1")
	if err != nil || !slices.Equal(got, []string{"tg-1", "tg-2"}) {
		t.Fatalf("labels %v, %v", got, err)
	}
	if _, err := b.Labels(context.Background(), "plan-none"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing blueprint gave %v", err)
	}
}

// groupRecord is a valid group state at a stage.
func groupRecord(id string, stage state.Stage, outcome string) state.GroupState {
	return state.GroupState{
		Run: testRun, Group: id, Stage: stage, Outcome: outcome, Epoch: 1, Fence: 1, ConfigHash: testHash,
		Counters:    state.Counters{RetriesLeft: 2, Blocks: map[string]int{}},
		Tasks:       []state.TaskState{{ID: "tsk-a", State: "done"}, {ID: "tsk-b", State: "queued"}},
		TestReview:  state.TestReview{Findings: []state.Finding{}, NotMet: []string{}, Failed: []string{}},
		ActionsOpen: []state.OpenAction{},
		Resume:      []state.ResumeRef{},
		Owed:        []string{},
		Requests:    []string{},
		ScopeAdded:  []string{},
		LastEventAt: time.Date(2026, 10, 14, 16, 1, 40, 0, time.UTC),
	}
}

// stateRig returns a state manager holding a running group idOne and a done group idTwo.
func stateRig(t *testing.T, records bool) *state.Manager {
	t.Helper()

	m, err := state.New(t.TempDir(), "engine")
	if err != nil {
		t.Fatal(err)
	}
	if !records {
		return m
	}
	if _, err := m.Create(idOne, groupRecord(idOne, state.StageBuild, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(idTwo, groupRecord(idTwo, state.StageDone, "complete")); err != nil {
		t.Fatal(err)
	}

	return m
}

func TestUnfinished(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writePlan(t, home, "plan-1")

	u := unfinished{st: stateRig(t, true), home: home, folder: intake.RepoFolder(testRepo)}

	t.Run("Unfinished names the plan of each group as its epic", func(t *testing.T) {
		t.Parallel()

		got, err := u.Unfinished(context.Background())
		if err != nil || len(got) != 1 || got[0].Group != idOne || got[0].Epic != "plan-1" {
			t.Fatalf("groups %+v, %v", got, err)
		}
	})

	t.Run("PlanGroup maps a label to the group state", func(t *testing.T) {
		t.Parallel()

		cases := map[string]cleanup.GroupStatus{"tg-1": cleanup.StatusRunning, "tg-2": cleanup.StatusClosed}
		for label, want := range cases {
			if got, err := u.PlanGroup(context.Background(), "plan-1", label); err != nil || got != want {
				t.Fatalf("%s: %q, %v, want %q", label, got, err, want)
			}
		}
		if _, err := u.PlanGroup(context.Background(), "plan-1", "tg-9"); err == nil {
			t.Fatal("an unknown label was accepted")
		}
		if _, err := u.PlanGroup(context.Background(), "plan-none", "tg-1"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a missing plan gave %v", err)
		}
	})

	t.Run("a group with no record has no status", func(t *testing.T) {
		t.Parallel()

		other := unfinished{st: stateRig(t, false), home: home, folder: intake.RepoFolder(testRepo)}
		if got, err := other.PlanGroup(context.Background(), "plan-1", "tg-1"); err != nil || got != cleanup.StatusNone {
			t.Fatalf("%q, %v", got, err)
		}
	})
}

func TestGroupLocks(t *testing.T) {
	t.Parallel()

	g := groupLocks{stateRig(t, true)}

	t.Run("InRun locks a running group and a done one", func(t *testing.T) {
		t.Parallel()

		for _, id := range []string{idOne, idTwo} {
			if run, locked, err := g.InRun(id); err != nil || !locked || run != testRun {
				t.Fatalf("%s: %q %v %v", id, run, locked, err)
			}
		}
		if _, locked, err := g.InRun(idNone); err != nil || locked {
			t.Fatalf("a group with no record: locked %v, %v", locked, err)
		}
	})

	t.Run("Finished names the done tasks", func(t *testing.T) {
		t.Parallel()

		if got, err := g.Finished(idOne); err != nil || !slices.Equal(got, []string{"tsk-a"}) {
			t.Fatalf("finished %v, %v", got, err)
		}
		if got, err := g.Finished(idNone); err != nil || got != nil {
			t.Fatalf("a group with no record: %v, %v", got, err)
		}
	})
}

func TestPlannedGroups(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writePlan(t, home, "plan-1")

	got := plannedGroups(home, intake.RepoFolder(testRepo))
	if got[idTwo] != (groupPlace{plan: "plan-1", label: "tg-2"}) {
		t.Fatalf("places %v", got)
	}
	if len(plannedGroups(home, "nowhere")) != 0 {
		t.Fatal("a repository with no plans has groups")
	}
}

func TestTaskSpecCarriesCriteriaAndSkipsAnEmptyDoneWhen(t *testing.T) {
	t.Parallel()

	cfg, err := config.New(
		t.TempDir(),
		config.Known{GuardRows: task.KnownRows(), Tools: task.KnownTools()},
		func(string) bool { return true },
	)
	if err != nil {
		t.Fatal(err)
	}

	k := &repoKit{s: &stack{cfg: cfg}}
	g := intake.Group{Name: "tg-1", Tasks: []intake.Task{
		{Name: "tsk-a", DoneWhen: "true", Acceptance: []intake.Acceptance{
			{Keyword: "WHEN", Text: "x THE SYSTEM SHALL y", Cmd: "go test ./..."},
		}},
		{Name: "tsk-b", Acceptance: []intake.Acceptance{{Keyword: "IF", Text: "p THE SYSTEM SHALL q"}}},
	}}
	cmds := map[string][]string{}

	ts, items := k.taskSpec(engine.GroupSpec{Label: "tg-1"}, g, cmds)

	if ts.Tasks[0].DoneWhen != "task:tsk-a" || ts.Tasks[1].DoneWhen != "" {
		t.Fatalf("done_when classes %q %q", ts.Tasks[0].DoneWhen, ts.Tasks[1].DoneWhen)
	}
	if _, ok := cmds["task:tsk-b"]; ok {
		t.Fatal("a task with no done_when got a command")
	}

	want := []task.Criterion{{Text: "WHEN x THE SYSTEM SHALL y", Cmd: "go test ./..."}}
	if !slices.Equal(ts.Tasks[0].Criteria, want) || len(ts.Tasks[1].Criteria) != 1 {
		t.Fatalf("criteria %+v / %+v", ts.Tasks[0].Criteria, ts.Tasks[1].Criteria)
	}
	if len(items) != 2 || items[1].ID != "acc-2" || items[0].Cmd != "go test ./..." {
		t.Fatalf("items %+v", items)
	}
}

func TestCleanItemsPrintsAPlan(t *testing.T) {
	t.Parallel()

	got := cleanItems(cleanup.Report{Removed: []cleanup.Item{{Kind: cleanup.KindPlan, Name: "/h/plans/r/plan-1"}}})
	if len(got) != 1 || got[0].Kind != "plan" || got[0].Name != "/h/plans/r/plan-1" {
		t.Fatalf("items %+v", got)
	}
}
