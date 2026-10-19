package task

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestScopeRequestEndsTheBuilderSessionWithReasonNeedsScope(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", sayResult(needsScope("lib/y.go")))
	if err := r.run(); !errors.Is(err, ErrHeld) {
		t.Fatalf("Run returned %v", err)
	}

	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()
	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" {
			continue
		}
		var d struct {
			Reason string   `json:"reason"`
			Paths  []string `json:"scope_paths"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason != "needs_scope" || len(d.Paths) != 1 || d.Paths[0] != "lib/y.go" {
			t.Fatalf("session.end carries %s, want reason needs_scope and the path asked for", f.Data)
		}
		return
	}
	t.Fatal("no session.end fact")
}

// builderEnds returns the reasons of the builder's session.end facts, each with its scope paths.
func builderEnds(t *testing.T, r *pipeRig) (reasons []string, paths [][]string) {
	t.Helper()
	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()
	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" {
			continue
		}
		var d struct {
			Agent  string   `json:"agent"`
			Reason string   `json:"reason"`
			Paths  []string `json:"scope_paths"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Agent == "builder" {
			reasons, paths = append(reasons, d.Reason), append(paths, d.Paths)
		}
	}
	return reasons, paths
}

func TestAutoDeniedScopeRequestDoesNotEndTheSessionNeedsScope(t *testing.T) {
	for _, p := range []string{"~/.komodo/x", ".env", "other/x.go"} {
		r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) { s.OtherFiles = []string{"other/**"} }})
		var inputs []string
		resumeWith(r, &inputs, builderDone)
		r.model.on("builder", sayResult(needsScope(p)))
		r.model.on("tester", sayResult(testerPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		r.reachCleanup()

		reasons, paths := builderEnds(t, r)
		if len(reasons) == 0 || reasons[0] != "done" || len(paths[0]) != 1 || paths[0][0] != p {
			t.Fatalf("%s: builder session ends %q with paths %q, want done carrying the denied path", p, reasons, paths)
		}
	}
}

func TestSecondScopeRequestEndsDoneWhileTheFirstEndsNeedsScope(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	var inputs []string
	resumeWith(r, &inputs, needsScope("lib/z.go"), builderDone)
	r.model.on("builder", sayResult(needsScope("lib/y.go")))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	if err := r.run(); !errors.Is(err, ErrHeld) {
		t.Fatalf("Run returned %v", err)
	}
	escs := r.rt.escalations()
	r.reachCleanupWith(func(ctx context.Context) error {
		return r.m.Continue(ctx, Answer{Escalation: escs[0].ID, Action: "approve", Approved: []string{"lib/y.go"}})
	})

	reasons, _ := builderEnds(t, r)
	if len(reasons) < 2 || reasons[0] != "needs_scope" || reasons[1] != "done" {
		t.Fatalf("builder session ends %q, want needs_scope then done", reasons)
	}
}
