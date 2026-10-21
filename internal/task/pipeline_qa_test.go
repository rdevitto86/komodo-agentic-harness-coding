package task

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// fakeDeep is a Deep that records the plans it was given and answers from a script.
type fakeDeep struct {
	mu    sync.Mutex
	plans [][]agent.DeepKind
	fail  agent.DeepKind // the check that fails; empty means none
	err   error
}

// Run records the plan and returns one result per check.
func (d *fakeDeep) Run(_ context.Context, _ agent.CommandRunner, plan []agent.DeepKind) ([]agent.DeepResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.plans = append(d.plans, slices.Clone(plan))
	if d.err != nil {
		return nil, d.err
	}

	var out []agent.DeepResult
	for _, k := range plan {
		r := agent.DeepResult{Kind: k, Pass: k != d.fail}
		if k == d.fail {
			r.Output = "WARNING: DATA RACE in " + string(k)
		}

		out = append(out, r)
	}

	return out, nil
}

// factData returns the data of the first fact with this subtype.
func (r *pipeRig) factData(sub string) (json.RawMessage, bool) {
	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()

	for _, f := range r.rt.facts {
		if f.Subtype == sub {
			return f.Data, true
		}
	}

	return nil, false
}

// A clean check result sets the risk level from the diff, logs its reason, and runs the deep checks of that level.
func TestRiskLevelPicksTheDeepPlanAndIsLogged(t *testing.T) {
	cases := []struct {
		file  string
		level agent.RiskLevel
	}{
		{"a.go", agent.RiskMedium},
		{"Makefile", agent.RiskCritical},
	}
	for _, c := range cases {
		d := &fakeDeep{}
		r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c2 *Collaborators) { c2.Deep = d }})
		r.model.on("builder", building(r.wt, c.file, "package a\n", builderDone))
		r.model.on("qa", sayResult(qaPass))
		r.model.on("reviewer", sayResult(reviewerMet))
		r.reachCleanup()

		if len(d.plans) != 1 || !slices.Equal(d.plans[0], agent.DeepPlan(c.level)) {
			t.Errorf("%s: plans %v, want %v", c.file, d.plans, agent.DeepPlan(c.level))
		}

		raw, ok := r.factData("risk.set")

		var rs struct{ Level, Reason string }
		_ = json.Unmarshal(raw, &rs)

		if !ok || rs.Level != string(c.level) || (c.level == agent.RiskCritical && !strings.Contains(rs.Reason, c.file)) {
			t.Errorf("%s: risk.set %s", c.file, raw)
		}
		if _, ok := r.factData("deep.done"); !ok {
			t.Errorf("%s: no deep.done fact", c.file)
		}
	}
}

// A race sends the group back to the Builder with the output, and neither judge starts.
func TestDeepFailureReturnsTheGroupWithoutAModelCall(t *testing.T) {
	d := &fakeDeep{fail: agent.DeepRace}
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) { c.Deep = d }})
	r.model.on(
		"builder",
		building(r.wt, "a.go", "package a\n", builderDone),
		building(r.wt, "b.go", "package b\n", builderDone),
		building(r.wt, "c.go", "package c\n", builderDone),
	)

	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if r.model.started("qa") != 0 || r.model.started("reviewer") != 0 || r.model.started("builder") < 2 {
		t.Fatalf("sessions %v", r.model.starts)
	}
	if got := r.slot("builder", 1, "findings"); !strings.Contains(got, "DATA RACE") {
		t.Errorf("the builder's findings lack the race output: %q", got)
	}
}

// A deep check that cannot run blocks the group setup, naming the check.
func TestDeepRunErrorBlocksSetup(t *testing.T) {
	d := &fakeDeep{err: context.DeadlineExceeded}
	r := newPipeRig(t, rigOpts{mut: func(_ *GroupSpec, c *Collaborators) { c.Deep = d }})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))

	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "setup" {
		t.Fatalf("escalations %+v", escs)
	}
}

// A pass whose evidence never replays restarts Quality Assurance once, and a second one blocks qa without a Builder try.
func TestUnreplayedPassRestartsOnceThenBlocksQA(t *testing.T) {
	items := []agent.Item{{ID: "a1", Text: "it works", Cmd: "test"}}
	bad := `{"items":[{"item":"a1","pass":true,"evidence":[{"cmd":"test","exit":1,"excerpt":""}]}],` +
		`"tasks":[{"task":"t1","confirmation":"confirmed"}]}`

	r := newPipeRig(t, rigOpts{items: items})
	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(bad), sayResult(bad))
	r.model.on("reviewer", sayResult(reviewerMet))

	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}
	if escs := r.rt.escalations(); len(escs) != 1 || escs[0].Reason != "qa" {
		t.Fatalf("escalations %+v", escs)
	}
	if r.model.started("qa") != 2 || r.model.started("builder") != 1 || r.rt.progress().Returns != 0 {
		t.Fatalf("sessions %v progress %+v", r.model.starts, r.rt.progress())
	}
	if got := r.slot("qa", 1, "checks"); !strings.Contains(got, "did not reproduce") {
		t.Errorf("the restart brief lacks Go's output: %q", got)
	}
}

// An approved group is stamped with a sign-off over its tree and test files; a changed test file voids it.
func TestSignoffIsStampedAndVoidedByATestEdit(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.model.on("builder", building(r.wt, "a_test.go", "package a\n", builderDone))
	r.model.on("qa", sayResult(qaPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	s, ok := r.m.Signoff()
	if !ok || s.Tree == "" || s.TestsHash == "" {
		t.Fatalf("sign-off %+v ok %v", s, ok)
	}

	writeFile(t, filepath.Join(r.wt, "a_test.go"), "package a // edited\n")
	if !r.m.SignoffVoided(context.Background()) {
		t.Error("an edited test file did not void the sign-off")
	}
}
