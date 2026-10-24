//go:build !windows

package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// readFile returns the text of a file or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// worktrees lists the registered worktrees of the fixture other than the main one and the epic's landing worktree.
func (r *binRig) worktrees() []string {
	var out []string
	for _, line := range strings.Split(r.git(r.repo, "worktree", "list", "--porcelain"), "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(r.repo); err == nil && (p == r.repo || p == resolved) {
			continue
		}
		if strings.HasPrefix(filepath.Base(p), "landing-") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// leaseFiles lists the lease records left under KOMODO_HOME/leases.
func (r *binRig) leaseFiles() []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(r.home, "leases"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasSuffix(p, ".lock") && filepath.Base(p) != ".fences.json" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// planGoal runs `komodo plan` on a goal and returns the plan id and the path of plan.md the plan folder holds.
func (r *binRig) planGoal(goal string) (id, path string) {
	r.t.Helper()

	res := r.komodo(90*time.Second, "plan", goal)
	if res.code != 0 {
		r.t.Fatalf("komodo plan exited %d\nstdout: %s\nstderr: %s\n%s", res.code, res.stdout, res.stderr, r.evidence(r.events()))
	}

	ids := r.plansWritten()
	if len(ids) != 1 {
		r.t.Fatalf("plan wrote %d plan folders, want 1: %v\nstdout: %s", len(ids), ids, res.stdout)
	}

	id = ids[0]
	path = filepath.Join(r.home, "plans", intake.RepoFolder(cli.RepoID(r.repo)), id, "plan.md")

	if !strings.Contains(res.stdout, id) || !strings.Contains(res.stdout, path) {
		r.t.Fatalf("plan printed neither the plan id %s nor the path %s\nstdout: %s", id, path, res.stdout)
	}

	return id, path
}

// Phase 6 exit: a planned group is published and cleaned up, driven through the real binary from `komodo plan`: the plan
// writes plan.md, and `komodo run <plan id>` then runs it.
func TestPhase6PlannedGroupIsPublishedAndCleanedUp(t *testing.T) {
	r := newBinRig(t, nil)
	id, path := r.planGoal("add an Add function to the tiny package")

	b, found := intake.Parse(readFile(t, path))
	if len(found) > 0 || b.Plan != id || len(b.Groups) != 1 || b.Groups[0].Name != "tg-1" {
		t.Fatalf("plan.md is not the planner's plan %s: %+v %v", id, b, found)
	}

	if res := r.komodo(60*time.Second, "run", id); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", id, res.code, res.stdout, res.stderr)
	}

	evs := r.waitDone(120 * time.Second)

	// Published: the epic branch holds the group's work locally, the publish facts are on the bus, and the pull request was
	// opened with gh.
	if out := r.git(r.origin, "show", "komodo/"+id+":tiny/add.go"); !strings.Contains(out, "func Add") {
		t.Fatalf("the epic branch lacks the published file: %q", out)
	}
	if !has(evs, "run-status", "publish.done", "") {
		t.Fatalf("no publish.done fact on the bus\n%s", r.evidence(evs))
	}
	if gh := r.ghCalls(); !strings.Contains(gh, "pr create") {
		t.Fatalf("the fake gh never opened a pull request; calls: %q", gh)
	}

	// Cleaned up: no group worktree, no group branch and no lease remain.
	if wt := r.worktrees(); len(wt) != 0 {
		t.Fatalf("group worktrees remain: %v", wt)
	}
	if br := r.git(r.repo, "branch", "--list", "komodo/"+id+"-*"); br != "" {
		t.Fatalf("group branches remain: %q", br)
	}
	if ls := r.leaseFiles(); len(ls) != 0 {
		t.Fatalf("leases remain: %v", ls)
	}
}
