package main

import (
	"slices"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestProgressOfCarriesTheTaskWithUnfinishedEdits(t *testing.T) {
	st := engine.State{Stage: state.StageBuild, Unfinished: &state.UnfinishedEdits{Task: "t2", Files: []string{"a.go"}}}
	p := progressOf(st)
	if p.Unfinished == nil || p.Unfinished.Task != "t2" || !slices.Equal(p.Unfinished.Files, []string{"a.go"}) {
		t.Fatalf("unfinished %+v", p.Unfinished)
	}
	st.Unfinished.Files[0] = "changed.go"
	if p.Unfinished.Files[0] != "a.go" {
		t.Fatal("the progress shares the saved record's file list")
	}
	if progressOf(engine.State{}).Unfinished != nil {
		t.Fatal("a group with no unfinished edits got some")
	}
}
