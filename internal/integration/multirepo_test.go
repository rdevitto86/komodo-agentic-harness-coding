//go:build !windows

package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// One engine serves groups from two repositories at once, each run carrying its own repository.
func TestOneEngineRunsGroupsFromTwoRepositories(t *testing.T) {
	a := newBinRig(t, nil)
	b := newBinRig(t, nil)
	// The second repository shares the first one's engine, home folder and model.
	b.home, b.model = a.home, a.model
	for i, e := range b.env {
		switch {
		case strings.HasPrefix(e, "KOMODO_HOME="):
			b.env[i] = "KOMODO_HOME=" + a.home
		case strings.HasPrefix(e, "ANTHROPIC_BASE_URL="):
			b.env[i] = "ANTHROPIC_BASE_URL=" + a.model.srv.URL
		}
	}
	a.writePlan()
	b.writePlanFile(tinyPlan, func(bp *intake.Blueprint) { bp.Groups[0].ID = "0192e4c5-7a10-7d3e-9b2f-000000000202" })
	for _, r := range []*binRig{a, b} {
		if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
			t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
		}
	}
	want := map[string]bool{"0192e4c5-7a10-7d3e-9b2f-000000000101": false, "0192e4c5-7a10-7d3e-9b2f-000000000202": false}
	for end := time.Now().Add(90 * time.Second); ; time.Sleep(300 * time.Millisecond) {
		evs := a.events()
		pushed := 0
		for _, e := range evs {
			if _, ok := want[e.Group]; ok && e.Type == "run-status" && e.Subtype == "group.done" {
				want[e.Group] = true
			}
			if e.Type == "run-status" && e.Subtype == "epic.pushed" {
				pushed++
			}
		}
		// Each epic is pushed once after its group is done, so both branches exist once both pushes are published.
		if want["0192e4c5-7a10-7d3e-9b2f-000000000101"] && want["0192e4c5-7a10-7d3e-9b2f-000000000202"] && pushed == 2 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("groups done: %v\n%s", want, a.evidence(evs))
		}
	}
	for _, r := range []*binRig{a, b} {
		if out := r.git(r.origin, "show", "komodo/"+fixtureEpic+":tiny/add.go"); !strings.Contains(out, "func Add") {
			t.Fatalf("the epic branch of %s lacks the builder's file: %q", r.repo, out)
		}
	}
}

// Two repositories on one engine each get their own metrics folder, and the machine folder lists both.
func TestMetricsEachRunWritesItsOwnRepositoryFolder(t *testing.T) {
	a := newBinRig(t, nil)
	b := newBinRig(t, nil)
	b.home, b.model = a.home, a.model
	for i, e := range b.env {
		switch {
		case strings.HasPrefix(e, "KOMODO_HOME="):
			b.env[i] = "KOMODO_HOME=" + a.home
		case strings.HasPrefix(e, "ANTHROPIC_BASE_URL="):
			b.env[i] = "ANTHROPIC_BASE_URL=" + a.model.srv.URL
		}
	}
	for _, r := range []*binRig{a, b} {
		r.writePlan()
		if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
			t.Fatalf("komodo run %s exited %d\nstdout: %s\nstderr: %s", fixturePlan, res.code, res.stdout, res.stderr)
		}
	}
	for end := time.Now().Add(90 * time.Second); ; time.Sleep(300 * time.Millisecond) {
		n := 0
		for _, r := range []*binRig{a, b} {
			if _, err := os.Stat(filepath.Join(r.repo, ".git", "komodo", "metrics", "runs.json")); err == nil {
				n++
			}
		}
		if n == 2 {
			break
		}
		if time.Now().After(end) {
			var found []string
			_ = filepath.WalkDir(a.home, func(p string, d fs.DirEntry, err error) error {
				if err == nil && strings.Contains(p, "metrics") {
					found = append(found, p)
				}
				return nil
			})
			t.Fatalf("a repository metrics folder lacks runs.json; machine metrics files: %v", found)
		}
	}
	for _, f := range []string{"runs.json", "groups.json", "sessions.jsonl"} {
		if _, err := os.Stat(filepath.Join(a.home, "metrics", f)); err == nil {
			t.Fatalf("the machine metrics folder holds %s, which belongs in a repository folder", f)
		}
	}
	raw, err := os.ReadFile(filepath.Join(a.home, "metrics", "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*binRig{a, b} {
		resolved, _ := filepath.EvalSymlinks(filepath.Join(r.repo, ".git"))
		if !strings.Contains(string(raw), filepath.Join(resolved, "komodo", "metrics")) {
			t.Fatalf("repos.json %s lacks the folder of %s", raw, r.repo)
		}
	}
}
