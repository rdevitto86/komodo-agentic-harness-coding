//go:build !windows

package integration

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestBinaryEngineServesStatus proves the binary's wiring: the CLI starts `komodo internal engine`, which binds the socket
// and answers status.
func TestBinaryEngineServesStatus(t *testing.T) {
	r := newBinRig(t, nil)
	res := r.komodo(60*time.Second, "status")
	if res.code != 0 {
		t.Fatalf("komodo status exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
}

// Phase 5 exit: one task group passes build, test and review, driven through the real binary and engine with a fake model.
func TestPhase5OneGroupPassesBuildTestAndReview(t *testing.T) {
	r := newBinRig(t, nil)
	r.writePlan()
	res := r.komodo(60*time.Second, "run", fixturePlan)
	if res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
	}
	evs := r.waitDone(40 * time.Second)

	// The group moves build, then test and review, then commit, then cleanup.
	got := stages(evs)
	want := []string{"build", "test_review", "commit", "cleanup"}
	last := -1
	for _, s := range want {
		i := slices.Index(got[last+1:], s)
		if i < 0 {
			t.Fatalf("stage %q missing after position %d in %v\n%s", s, last, got, r.evidence(evs))
		}
		last += i + 1
	}
	if !has(evs, "run-status", "group.done", "complete") {
		t.Fatalf("the group did not end complete\n%s", r.evidence(evs))
	}

	// The model saw a builder, a Quality Assurance and a reviewer session.
	calls := r.model.callCounts()
	for _, role := range []string{"builder", "qa", "reviewer"} {
		if calls[role] == 0 {
			t.Fatalf("no %s call reached the model: %v", role, calls)
		}
	}

	// The builder file is on the epic branch the Commit Handler pushed to the remote.
	if out := r.git(r.origin, "show", "komodo/"+fixtureEpic+":tiny/add.go"); !strings.Contains(out, "func Add") {
		t.Fatalf("the epic branch lacks the builder's file: %q", out)
	}
}
