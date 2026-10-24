package task

import (
	"slices"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestGroupWhoseRiskSkipsTheReviewerRunsNoReviewerSession(t *testing.T) {
	var asked []string
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) {
		c.SkipReview = func(diff string) bool {
			asked = append(asked, diff)
			return true
		}
	}})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.reachCleanup()

	want := []state.Stage{state.StageBuild, state.StageTestReview, state.StageCommit, state.StageCleanup, state.StageDone}
	if got := r.rt.stagesSeen(); !slices.Equal(got, want) {
		t.Fatalf("stages %v, want %v", got, want)
	}
	if n := r.model.started("reviewer"); n != 0 {
		t.Fatalf("%d reviewer sessions, want 0", n)
	}
	if n := r.model.total(); n != 2 {
		t.Fatalf("%d sessions, want 2 (builder, Quality Assurance)", n)
	}
	if len(asked) == 0 || asked[0] == "" {
		t.Errorf("the skip rule was not given the group's diff: %q", asked)
	}
	if pubs := r.cm.published(); len(pubs) != 1 {
		t.Fatalf("Publish called %d times, want 1", len(pubs))
	}
}
