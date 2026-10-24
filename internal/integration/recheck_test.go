//go:build !windows

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// noDocker points the Docker client at a socket that does not exist, so Quality Assurance uses the operating-system sandbox.
const noDocker = "DOCKER_HOST=unix:///nonexistent/docker.sock"

// epicLacksFile passes while the epic branch lacks the group's file and fails once the landing put it there.
const epicLacksFile = `! git cat-file -e komodo/` + fixtureEpic + `:tiny/add.go`

// waitFor polls the event store until an event of the type and subtype appears or the timeout fails the test.
func (r *binRig) waitFor(timeout time.Duration, typ, sub string) []event {
	r.t.Helper()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if evs := r.events(); has(evs, typ, sub, "") {
			return evs
		}
	}
	r.t.Fatalf("no %s/%s within %s\n%s", typ, sub, timeout, r.evidence(r.events()))
	return nil
}

// A landing whose free check fails on the epic tip pauses the epic and opens no pull request.
func TestRecheckFailureStopsTheLanding(t *testing.T) {
	r := newBinRig(t, nil)
	r.writePlanFile(tinyPlan, func(b *intake.Blueprint) { b.Groups[0].Tasks[0].DoneWhen = epicLacksFile })
	if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
	}
	evs := r.waitFor(60*time.Second, "run-status", "epic.paused")
	if has(evs, "run-status", "publish.done", "") {
		t.Fatalf("a failed recheck still reported publish.done\n%s", r.evidence(evs))
	}
	if gh := r.ghCalls(); strings.Contains(gh, "pr create") {
		t.Fatalf("a pull request opened after a failed recheck: %q", gh)
	}
}

// A landing whose free checks pass runs them on the epic tip, then opens the pull request.
func TestRecheckPassLetsTheLandingFinish(t *testing.T) {
	r := newBinRig(t, nil)
	r.writePlan()
	if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
	}
	evs := r.waitDone(90 * time.Second)
	if has(evs, "run-status", "epic.paused", "") {
		t.Fatalf("a passing recheck paused the epic\n%s", r.evidence(evs))
	}
	if !has(evs, "run-status", "publish.done", "") {
		t.Fatalf("no publish.done fact\n%s", r.evidence(evs))
	}
	if gh := r.ghCalls(); !strings.Contains(gh, "pr create") {
		t.Fatalf("no pull request opened; gh calls: %q", gh)
	}
}
