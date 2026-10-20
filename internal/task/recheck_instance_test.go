package task

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// probeChecks runs one command through the runner it is given, records the instances released by then, and returns a
// scripted outcome: "pass", "fail" or "error".
type probeChecks struct {
	mu       sync.Mutex
	be       *hostBackend
	mode     string
	exit     int
	runErr   error
	released []string
}

func (p *probeChecks) Check(ctx context.Context, run agent.CommandRunner, _ agent.CheckInput) (agent.CheckResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.exit, _, p.runErr = run(ctx, "true")

	p.be.mu.Lock()
	p.released = slices.Clone(p.be.released)
	p.be.mu.Unlock()

	switch p.mode {
	case "error":
		return agent.CheckResult{}, errors.New("the checker broke")
	case "fail":
		return agent.CheckResult{Steps: []agent.StepResult{
			{Step: agent.StepDoneWhen, Level: agent.LevelFail, Detail: []string{"task t1"}, Output: "FAIL: exact output"},
		}}, nil
	}

	return agent.CheckResult{Steps: []agent.StepResult{{Step: agent.StepDoneWhen, Level: agent.LevelPass}}}, nil
}

// leaked returns the instances the backend prepared and never released.
func leaked(be *hostBackend) []string {
	be.mu.Lock()
	defer be.mu.Unlock()

	var left []string
	for i := 1; i <= be.n; i++ {
		if id := fmt.Sprintf("inst-%d", i); !slices.Contains(be.released, id) {
			left = append(left, id)
		}
	}

	return left
}

// A recheck runs in an instance made after the group's cached tester instance and destroys it when it ends, whatever the
// verdict.
func TestRecheckRunsInAFreshInstanceAndDestroysItOnEveryOutcome(t *testing.T) {
	for _, mode := range []string{"pass", "fail", "error"} {
		t.Run(mode, func(t *testing.T) {
			r := newPipeRig(t, rigOpts{})
			probe := &probeChecks{be: r.be, mode: mode}
			r.m.c.Checks = probe

			var verdict string
			r.m.c.OnRecheck = func(pass bool, out string) {
				verdict = map[bool]string{true: "pass", false: "fail"}[pass] + ":" + out
			}

			if _, err := r.m.instance(context.Background(), "tester"); err != nil {
				t.Fatal(err)
			}

			r.m.Recheck(context.Background())

			if r.be.n != 2 {
				t.Fatalf("recheck prepared %d instances in total, want a second one besides the cached tester", r.be.n)
			}
			if probe.runErr != nil || probe.exit != 0 {
				t.Fatalf("the check command did not run in the recheck instance: exit %d err %v", probe.exit, probe.runErr)
			}
			if len(probe.released) != 0 {
				t.Fatalf("an instance was released before the checks ended: %v", probe.released)
			}
			if !slices.Equal(r.be.released, []string{"inst-2"}) {
				t.Fatalf("released %v, want only the recheck instance inst-2", r.be.released)
			}
			want := map[string]string{"pass": "pass:", "fail": "fail:", "error": "fail:"}[mode]
			if !strings.HasPrefix(verdict, want) {
				t.Fatalf("verdict %q for mode %s, want prefix %q", verdict, mode, want)
			}
			if got := leaked(r.be); !slices.Equal(got, []string{"inst-1"}) {
				t.Fatalf("after the recheck the live instances are %v, want only the cached tester inst-1", got)
			}
		})
	}
}

// Repeated rechecks each make and destroy their own instance, and closing the group leaves none behind.
func TestCloseTaskGroupLeavesNoInstanceAfterRechecks(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	r.m.c.Checks = &probeChecks{be: r.be, mode: "pass"}
	r.m.c.OnRecheck = func(bool, string) {}

	for _, role := range []string{"builder", "tester"} {
		if _, err := r.m.instance(context.Background(), role); err != nil {
			t.Fatal(err)
		}
	}

	r.m.Recheck(context.Background())

	if err := r.m.CloseTaskGroup(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The landing's recheck runs after the group closed.
	r.m.Recheck(context.Background())
	r.m.Recheck(context.Background())

	if got := leaked(r.be); len(got) != 0 {
		t.Fatalf("instances left after close and rechecks: %v of %d prepared", got, r.be.n)
	}
	if r.be.n != 5 {
		t.Fatalf("prepared %d instances, want 5 (builder, tester and three rechecks)", r.be.n)
	}
}

// An instance asked for after the group closed is refused and destroyed, so no container outlives the group.
func TestInstanceAfterCloseIsRefusedAndLeavesNothing(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if err := r.m.CloseTaskGroup(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := r.m.instance(context.Background(), "tester"); err == nil {
		t.Fatal("an instance was handed out after the group closed")
	}
	if got := leaked(r.be); len(got) != 0 {
		t.Fatalf("instances left after a refused call: %v", got)
	}
}
