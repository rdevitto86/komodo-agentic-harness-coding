package task

import "testing"

// A new group tells the Runtime Manager it is ready once its worktree exists.
func TestNewGroupReportsReadyOnce(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "src_a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if n := r.rt.factCount("ready"); n != 1 {
		t.Fatalf("ready facts %d, want 1", n)
	}
}

// Each stage the group leaves forward is reported as stage.done, the fact the Runtime Manager advances on.
func TestForwardStagesReportStageDone(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "src_a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()
	if n := r.rt.factCount("stage.done"); n != 3 {
		t.Fatalf("stage.done facts %d, want 3 (build, test_review, commit)", n)
	}
}
