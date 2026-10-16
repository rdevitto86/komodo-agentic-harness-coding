package integration

import (
	"strings"
	"testing"
	"time"
)

// One engine serves groups from two repositories at once, each run carrying its own repository.
func TestOneEngineRunsGroupsFromTwoRepositories(t *testing.T) {
	a := newBinRig(t, planFiles())
	other := planFiles()
	for p, body := range other {
		other[p] = strings.ReplaceAll(body, "000000000101", "000000000202")
	}
	b := newBinRig(t, other)
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
	for _, r := range []*binRig{a, b} {
		if res := r.komodo(60*time.Second, "run", "ep-1"); res.code != 0 {
			t.Fatalf("komodo run ep-1 exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
		}
	}
	want := map[string]bool{"0192e4c5-7a10-7d3e-9b2f-000000000101": false, "0192e4c5-7a10-7d3e-9b2f-000000000202": false}
	for end := time.Now().Add(90 * time.Second); ; time.Sleep(300 * time.Millisecond) {
		evs := a.events()
		for _, e := range evs {
			if _, ok := want[e.Group]; ok && e.Type == "run-status" && e.Subtype == "group.done" {
				want[e.Group] = true
			}
		}
		if want["0192e4c5-7a10-7d3e-9b2f-000000000101"] && want["0192e4c5-7a10-7d3e-9b2f-000000000202"] {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("groups done: %v\n%s", want, a.evidence(evs))
		}
	}
	for _, r := range []*binRig{a, b} {
		if out := r.git(r.origin, "show", "komodo/ep-1:tiny/add.go"); !strings.Contains(out, "func Add") {
			t.Fatalf("the epic branch of %s lacks the builder's file: %q", r.repo, out)
		}
	}
}
