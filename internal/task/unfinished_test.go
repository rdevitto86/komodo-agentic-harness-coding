package task

import (
	"context"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestBuilderBriefNamesTheTaskWithUnfinishedEdits(t *testing.T) {
	b := testRoles(t)["builder"]
	s, err := b.Slots(context.Background(), RoleInput{
		Unfinished: &state.UnfinishedEdits{Task: "t1", Files: []string{"a.go", "dir/b.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s["unfinished"]; !strings.Contains(got, "t1") || !strings.Contains(got, "a.go\ndir/b.go") {
		t.Fatalf("unfinished slot: %q", got)
	}
	if s, _ = b.Slots(context.Background(), RoleInput{}); s["unfinished"] != "" {
		t.Fatalf("a builder with nothing left behind got %q", s["unfinished"])
	}
}

func TestResumedBuilderIsToldWhichEditsAreInTheWorktree(t *testing.T) {
	left := &state.UnfinishedEdits{Task: "t1", Files: []string{"a.go"}}
	r := newPipeRig(t, rigOpts{saved: &Progress{Stage: state.StageBuild, Unfinished: left}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.briefs.slotsOf("builder", 0)["unfinished"]; !strings.Contains(got, "t1") || !strings.Contains(got, "a.go") {
		t.Fatalf("first builder brief: unfinished slot %q", got)
	}
	if r.m.Progress().Unfinished != nil {
		t.Fatalf("the finished task still counts as unfinished: %+v", r.m.Progress().Unfinished)
	}
}

func TestNextBuilderSessionIsToldWhatTheLastOneLeftBehind(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: 2}})
	r.git.statuses = []string{" M a.go\n?? dir/b.go\n"}
	r.model.on("builder", sayResult(`{"items":[]}`), building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if got := r.briefs.slotsOf("builder", 0)["unfinished"]; got != "" {
		t.Fatalf("first brief: unfinished slot %q", got)
	}
	if got := r.briefs.slotsOf("builder", 1)["unfinished"]; !strings.Contains(got, "a.go\ndir/b.go") {
		t.Fatalf("second brief: unfinished slot %q", got)
	}
}
