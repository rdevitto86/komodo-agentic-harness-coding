package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

func TestScopeRequestForAProtectedSecretOrOtherGroupPathIsDeniedWithoutEscalating(t *testing.T) {
	for _, p := range []string{"~/.komodo/x", ".env", "internal/task/policy.json", "other/x.go"} {
		r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) { s.OtherFiles = []string{"other/**"} }})
		var inputs []string
		resumeWith(r, &inputs, builderDone)
		r.model.on("builder", sayResult(needsScope(p)))
		r.model.on("tester", sayResult(testerPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		r.reachCleanup()
		if len(r.rt.escalations()) != 0 {
			t.Fatalf("%s: escalated %+v", p, r.rt.escalations())
		}
		if len(inputs) != 1 || !strings.Contains(inputs[0], "scope request denied") || r.rt.factCount("scope.request") != 1 {
			t.Fatalf("%s: resume inputs %q", p, inputs)
		}
	}
}

func TestSecondScopeRequestFromOneTaskIsDeniedAtOnce(t *testing.T) {
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
	if len(escs) != 1 || escs[0].Kind != EscScope || escs[0].Reason != "needs_scope" ||
		!strings.Contains(string(escs[0].Detail), "lib/y.go") {
		t.Fatalf("escalations %+v", escs)
	}
	r.reachCleanupWith(func(ctx context.Context) error {
		return r.m.Continue(ctx, Answer{Escalation: escs[0].ID, Action: "approve", Approved: []string{"lib/y.go"}})
	})
	if len(r.rt.escalations()) != 1 {
		t.Fatalf("the second request escalated: %+v", r.rt.escalations())
	}
	if len(inputs) != 2 || !strings.Contains(inputs[0], "approved for: lib/y.go") ||
		!strings.Contains(inputs[1], "one scope request per task") {
		t.Fatalf("resume inputs %q", inputs)
	}
}

func TestApprovedProtectedPathIsNotAppliedAndAnApprovedOneReachesScopeJSONAndTheGuard(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	var inputs []string
	resumeWith(r, &inputs, builderDone)
	r.model.on("builder", sayResult(needsScope("lib/y.go")))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	if err := r.run(); !errors.Is(err, ErrHeld) {
		t.Fatalf("Run returned %v", err)
	}
	g, err := r.m.scopeGuard()
	if err != nil {
		t.Fatal(err)
	}
	write := Call{Tool: "write", Args: json.RawMessage(`{"path":"lib/y.go","content":"x"}`)}
	if v := g.Check(write); v.Allowed || v.Row != "B1" {
		t.Fatalf("before the answer the write is %+v", v)
	}
	id := r.rt.escalations()[0].ID
	r.reachCleanupWith(func(ctx context.Context) error {
		return r.m.Continue(ctx, Answer{
			Escalation: id,
			Action:     "approve",
			Approved:   []string{"lib/y.go", "internal/task/policy.json"},
		})
	})
	if got := r.rt.progress().Widened; !slices.Equal(got, []string{"lib/y.go"}) {
		t.Fatalf("widened %v", got)
	}
	g, err = r.m.scopeGuard()
	if err != nil {
		t.Fatal(err)
	}
	if v := g.Check(write); !v.Allowed {
		t.Fatalf("after the answer the write is %+v", v)
	}
	if v := g.Check(Call{
		Tool: "write",
		Args: json.RawMessage(`{"path":"internal/task/policy.json","content":"x"}`),
	}); v.Allowed {
		t.Fatal("a protected path was applied")
	}
	var found bool
	dirs, _ := filepath.Glob(filepath.Join(r.m.c.SessionsDir, "*", "scope.json"))
	for _, d := range dirs {
		b, _ := os.ReadFile(d)
		found = found || strings.Contains(string(b), "lib/y.go")
	}
	if !found {
		t.Fatal("no scope.json holds the added path")
	}
}

func TestDeniedScopeAnswerResumesTheBuilderWithADenial(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	var inputs []string
	resumeWith(r, &inputs, builderDone)
	r.model.on("builder", sayResult(needsScope("lib/y.go")))
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	if err := r.run(); !errors.Is(err, ErrHeld) {
		t.Fatalf("Run returned %v", err)
	}
	id := r.rt.escalations()[0].ID
	r.reachCleanupWith(func(ctx context.Context) error {
		return r.m.Continue(ctx, Answer{Escalation: id, Action: "deny"})
	})
	if len(inputs) != 1 || inputs[0] != "scope request denied" || len(r.rt.progress().Widened) != 0 {
		t.Fatalf("inputs %q, widened %v", inputs, r.rt.progress().Widened)
	}
}

// needsScope is a builder result that asks for these paths.
func needsScope(paths ...string) string {
	b, _ := json.Marshal(map[string]any{
		"status":  "needs_scope",
		"summary": "blocked",
		"paths":   paths,
		"reason":  "the task needs them",
	})
	return string(b)
}

// resumeWith makes the scripted model answer a resume with a builder result, keeping the input it got.
func resumeWith(r *pipeRig, in *[]string, steps ...string) {
	r.model.resume = func(_ llm.Session, i llm.Input) (llm.Stream, error) {
		*in = append(*in, i.Text)
		out := steps[0]
		if len(steps) > 1 {
			steps = steps[1:]
		}
		return building(r.wt, "a.go", "package a\n", out)(llm.Session{})
	}
}
