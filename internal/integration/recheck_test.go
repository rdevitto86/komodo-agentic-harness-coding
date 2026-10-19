//go:build !windows

package integration

import (
	"strings"
	"testing"
	"time"
)

// noDocker points the Docker client at a socket that does not exist, so the Tester uses the operating-system sandbox.
const noDocker = "DOCKER_HOST=unix:///nonexistent/docker.sock"

// epicLacksFile passes while the epic branch lacks the group's file and fails once the landing put it there.
const epicLacksFile = `! git cat-file -e komodo/ep-1:tiny/add.go`

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
	files := planFiles()
	files["docs/backlog/epic-1/tg-1.1/tsk-add.md"] = strings.Replace(
		files["docs/backlog/epic-1/tg-1.1/tsk-add.md"],
		"done_when: test -f tiny/add.go",
		"done_when: "+epicLacksFile,
		1,
	)
	r := newBinRig(t, files)
	// The check needs git, which a Tester container never holds, so this run uses the operating-system sandbox.
	r.env = append(r.env, noDocker)
	if res := r.komodo(60*time.Second, "run", "ep-1"); res.code != 0 {
		t.Fatalf("komodo run ep-1 exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
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
	r := newBinRig(t, planFiles())
	if res := r.komodo(60*time.Second, "run", "ep-1"); res.code != 0 {
		t.Fatalf("komodo run ep-1 exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
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
