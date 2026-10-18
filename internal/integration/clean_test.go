package integration

import (
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
)

// A new engine's `komodo clean` removes the worktree and container a killed engine left behind.
func TestCleanSweepsWhatAKilledEngineLeft(t *testing.T) {
	r := newBinRig(t, planFiles())
	boxes := r.writeDocker()
	if res := r.komodo(60*time.Second, "status"); res.code != 0 {
		t.Fatalf("komodo status exited %d: %s %s", res.code, res.stdout, res.stderr)
	}
	r.killEngine()
	const stray = "0192e4c5-7a10-7d3e-9b2f-0000000009ff"
	wt := filepath.Join(r.home, "wt", path.Base(cli.RepoID(r.repo)), stray)
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "worktree", "add", "-q", "-b", "komodo/ep-1-tg-9.9", wt, "main")
	box := filepath.Join(boxes, "komodo-"+stray+"-abcd1234")
	if err := os.WriteFile(box, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := r.komodo(90*time.Second, "clean"); res.code != 0 {
		t.Fatalf("komodo clean exited %d\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		_, werr := os.Stat(wt)
		_, berr := os.Stat(box)
		if werr != nil && berr != nil {
			return
		}
		if time.Now().After(end) {
			t.Fatalf(
				"clean left the worktree (%v) or the container (%v)\n%s",
				werr == nil,
				berr == nil,
				r.evidence(r.events()),
			)
		}
	}
}
