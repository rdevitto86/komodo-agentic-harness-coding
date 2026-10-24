//go:build !windows

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Abandoning a running group frees its worktree, branch, lease and container.
func TestAbandonFreesARunningGroupsResources(t *testing.T) {
	r := newBinRig(t, nil)
	boxes := r.writeDocker()
	const group = "0192e4c5-7a10-7d3e-9b2f-000000000101"
	box := filepath.Join(boxes, "komodo-"+group+"-abcd1234")
	if err := os.WriteFile(box, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A container of another group whose name starts with this group's name must survive.
	neighbour := filepath.Join(boxes, "komodo-"+group+"-2-abcd1234")
	if err := os.WriteFile(neighbour, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	started, release := r.model.arm()
	t.Cleanup(release)
	r.writePlan()
	if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
	}
	select {
	case <-started:
	case <-time.After(40 * time.Second):
		t.Fatalf("the builder never started\n%s", r.evidence(r.events()))
	}
	if res := r.komodo(60*time.Second, "abandon", fixturePlan, fixtureGroup); res.code != 0 {
		t.Fatalf("komodo abandon exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var left []string
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		left = left[:0]
		left = append(left, r.worktrees()...)
		if br := r.git(r.repo, "branch", "--list", "komodo/"+fixtureEpic+"-*"); br != "" {
			left = append(left, "branch "+br)
		}
		left = append(left, r.leaseFiles()...)
		if _, err := os.Stat(box); err == nil {
			left = append(left, "container "+box)
		}
		if len(left) == 0 {
			if _, err := os.Stat(neighbour); err != nil {
				t.Fatalf("abandon removed the container of another group: %v", err)
			}
			return
		}
	}
	t.Fatalf("abandon left behind: %v\n%s", left, r.evidence(r.events()))
}
