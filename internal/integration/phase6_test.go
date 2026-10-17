package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Phase 6 exit: a planned group is published and cleaned up, driven through the real binary from `komodo plan`.
func TestPhase6PlannedGroupIsPublishedAndCleanedUp(t *testing.T) {
	r := newBinRig(t, nil)
	res := r.komodo(90*time.Second, "plan", "add an Add function to the tiny package")
	if res.code != 0 {
		t.Fatalf("komodo plan exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	tg := filepath.Join(r.repo, "docs", "backlog", "epic-1", "tg-1.1", "TG.md")
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if _, err := os.Stat(tg); err == nil {
			break
		}
		if time.Now().After(end) {
			r.signalEngine(syscall.SIGUSR1)
			time.Sleep(time.Second)
			t.Fatalf("the plan wrote no task list; model calls %v\n%s", r.model.callCounts(), r.evidence(r.events()))
		}
	}
	evs := r.waitDone(120 * time.Second)

	// Published: the epic branch holds the group's work locally, the publish facts are on the bus, and the pull request was opened with gh.
	if out := r.git(r.origin, "show", "komodo/ep-1:tiny/add.go"); !strings.Contains(out, "func Add") {
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
	if br := r.git(r.repo, "branch", "--list", "komodo/ep-1-*"); br != "" {
		t.Fatalf("group branches remain: %q", br)
	}
	if ls := r.leaseFiles(); len(ls) != 0 {
		t.Fatalf("leases remain: %v", ls)
	}
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
