//go:build !windows

package integration

import (
	"os"
	"strings"
	"testing"
	"time"
)

// waitUntil polls the event store until ok holds for its events; the timeout fails the test with the evidence.
func (r *binRig) waitUntil(timeout time.Duration, what string, ok func([]event) bool) []event {
	r.t.Helper()

	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if evs := r.events(); ok(evs) {
			return evs
		}
	}

	r.t.Fatalf("%s did not happen within %s\n%s", what, timeout, r.evidence(r.events()))

	return nil
}

// groupHas reports whether the group has an event of the type and subtype.
func groupHas(evs []event, group, typ, sub string) bool {
	for _, e := range evs {
		if e.Group == group && e.Type == typ && e.Subtype == sub {
			return true
		}
	}

	return false
}

// riskyBuilder scripts the fake builder for the risky plan: each task writes its one file, the add task also its test, and
// the task ends with task_done.
func riskyBuilder(brief string, turn int) (string, map[string]any) {
	task := firstMatch(taskLine, brief)
	files := map[string][][2]string{
		"tsk-ci": {{".github/workflows/ci.yml", "name: ci\non: push\njobs: {}\n"}},
		"tsk-add": {
			{"tiny/add.go", "package tiny\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a + b }\n"},
			{"tiny/add_test.go", "package tiny\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n" +
				"\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n"},
		},
		"tsk-dep": {{"tiny/dep.go", "package tiny\n\n// Dep returns 2.\nfunc Dep() int { return 2 }\n"}},
	}[task]

	switch {
	case turn < len(files):
		return "write", map[string]any{"path": files[turn][0], "content": files[turn][1]}
	case turn == len(files) && task != "":
		return "task_done", map[string]any{"task": task}
	}

	return "", nil
}

// Plan approval: `komodo plan` prints the plan id and the path of plan.md and starts no run; nothing runs until
// `komodo run <plan id>`, which refuses a plan id the repository has no plan for.
func TestPlanApprovalPlanWritesAndStops(t *testing.T) {
	r := newBinRig(t, nil)
	id, path := r.planGoal("add an Add function to the tiny package")

	if !strings.HasPrefix(id, "plan-") || len(id) != len("plan-0a1b2c") {
		t.Fatalf("plan id %q is not plan- and 6 hexadecimal characters", id)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plan.md is missing at the printed path: %v", err)
	}

	// The engine has had time to start a run if plan had queued one.
	time.Sleep(3 * time.Second)

	evs := r.events()
	if has(evs, "run-status", "run.start", "") || has(evs, "run-start", "", "") {
		t.Fatalf("plan started a run\n%s", r.evidence(evs))
	}

	if calls := r.model.callCounts(); calls["planner"] == 0 || calls["builder"] != 0 {
		t.Fatalf("model calls %v, want planner calls and 0 builder calls", calls)
	}

	res := r.komodo(60*time.Second, "run", "plan-ffffff")
	if res.code == 0 || !strings.Contains(res.stdout, "no plan plan-ffffff") {
		t.Fatalf("run of an unknown plan id exited %d\nstdout: %s", res.code, res.stdout)
	}

	if res := r.komodo(60*time.Second, "run", id); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", id, res.code, res.stdout, res.stderr)
	}

	r.waitDone(120 * time.Second)
}

// A risky group blocks with "land this risky change?", an independent group lands, the dependent group waits, and
// `komodo run <plan id> --approve` lands the risky group on the epic branch and starts the dependent group.
func TestRiskyGroupBlocksThenLandsOnApprove(t *testing.T) {
	const (
		risky       = "0192e4c5-7a10-7d3e-9b2f-000000000401"
		independent = "0192e4c5-7a10-7d3e-9b2f-000000000402"
		dependent   = "0192e4c5-7a10-7d3e-9b2f-000000000403"
	)

	r := newBinRig(t, nil)
	r.model.builder = riskyBuilder
	r.writePlanFile("risky.md", nil)

	if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
	}

	evs := r.waitUntil(150*time.Second, "the risky group blocked and the independent group landed", func(evs []event) bool {
		return has(evs, "run-failure", "group.blocked", "risky_change") && groupHas(evs, independent, "run-status", "group.done")
	})

	if groupHas(evs, dependent, "run-status", "session.start") {
		t.Fatalf("the dependent group started before the risky group landed\n%s", r.evidence(evs))
	}

	if groupHas(evs, risky, "run-status", "group.done") {
		t.Fatalf("the risky group ended without an approval\n%s", r.evidence(evs))
	}

	res := r.komodo(60*time.Second, "run", fixturePlan, "--approve")
	if res.code != 0 || !strings.Contains(res.stdout, "Landed risky groups") || !strings.Contains(res.stdout, "tg-1") {
		t.Fatalf("--approve exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}

	// The approved group leaves the block and enters Commit.
	r.waitUntil(60*time.Second, "the risky group resuming", func(evs []event) bool {
		return groupHas(evs, risky, "run-status", "group.resumed")
	})
}
