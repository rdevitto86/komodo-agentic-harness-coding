//go:build e2e && !windows

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The checks in this file are the slow end-to-end checks of the verification plan; run them with
// `go test -tags e2e ./internal/integration/`.

// terminal runs the binary under a pseudo terminal, which install and config set need.
func (r *binRig) terminal(timeout time.Duration, args ...string) result {
	r.t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("script", append([]string{"-q", "/dev/null", r.bin}, args...)...)
	} else {
		cmd = exec.Command("script", "-qec", r.bin+" "+strings.Join(args, " "), "/dev/null")
	}
	cmd.Dir, cmd.Env = r.repo, r.env
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		r.t.Fatalf("komodo %s under a terminal did not finish in %s", strings.Join(args, " "), timeout)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return result{code: code, stdout: string(out)}
}

// repoRoot returns the root of this repository.
func repoRoot() string { return filepath.Join(mustWd(), "..", "..") }

// goTool runs the go command in the repository root and fails the test on a non-zero exit.
func goTool(t *testing.T, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot()
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// subseq reports whether want appears in got in order, and returns the first missing key.
func subseq(got, want []string) (string, bool) {
	at := 0
	for _, w := range want {
		i := slices.Index(got[at:], w)
		if i < 0 {
			return w, false
		}
		at += i + 1
	}
	return "", true
}

// keys renders each event as type/subtype.
func keys(evs []event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type+"/"+e.Subtype)
	}
	return out
}

// Every phase: go vet, go test and a build for each of the 3 operating systems all pass.
func TestEveryPhaseVetTestAndThreeBuilds(t *testing.T) {
	goTool(t, nil, "vet", "./...")
	// This package is the one running now, so the test pass covers every other package.
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = repoRoot()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []string
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasSuffix(p, "/internal/integration") {
			pkgs = append(pkgs, p)
		}
	}
	goTool(t, nil, append([]string{"test", "-count=1"}, pkgs...)...)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		goTool(t, []string{"GOOS=" + goos, "GOARCH=amd64", "CGO_ENABLED=0"}, "build", "-o", os.DevNull, "./cmd/komodo")
	}
}

// groupLife is the ordered list of events one group's life puts on the bus in a full run.
var groupLife = []string{
	"run-status/run.start", "run-status/stage.enter", "run-status/session.start", "run-status/task.done",
	"run-status/session.end", "run-status/check.done", "run-status/group.approved", "run-status/publish.done",
	"run-status/cleanup.start", "run-status/cleanup.done", "run-status/group.done", "run-status/epic.pushed",
	"run-status/pr.opened", "run-complete/done",
}

// Event round trip: every event of a group's life arrives once, in order, and the event store holds a line for each.
func TestEventRoundTripOnTheFixtureGroup(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	if res := r.komodo(60*time.Second, "run", fixturePlan); res.code != 0 {
		t.Fatalf("komodo run %s exited %d\n%s\n%s", fixturePlan, res.code, res.stdout, res.stderr)
	}
	evs := r.waitFor(90*time.Second, "run-complete", "done")
	got := keys(evs)
	if miss, ok := subseq(got, groupLife); !ok {
		t.Fatalf("event %s missing from the stored order\n%v", miss, got)
	}
	// Each fact that happens once in a group's life arrives once.
	for _, once := range []string{
		"run-status/run.start", "run-status/group.approved", "run-status/publish.done", "run-status/pr.opened",
		"run-status/cleanup.start", "run-status/cleanup.done", "run-status/group.done", "run-complete/done",
	} {
		if n := countOf(got, once); n != 1 {
			t.Errorf("%s arrived %d times, want 1", once, n)
		}
	}
	// Each repeating event arrives once per cause: per task, per stage, per agent session and per check round.
	for _, task := range []string{"tsk-sub", "tsk-mul"} {
		if n := countWith(evs, "task.done", `"task":"`+task+`"`); n != 1 {
			t.Errorf("task.done for %s arrived %d times, want 1", task, n)
		}
	}
	for _, to := range []string{"intake", "build", "test_review", "commit", "cleanup"} {
		if n := countWith(evs, "stage.enter", `"to":"`+to+`"`); n != 1 {
			t.Errorf("stage.enter to %s arrived %d times, want 1", to, n)
		}
	}
	for _, agent := range []string{"builder", "qa", "reviewer"} {
		if n := countWith(evs, "session.start", `"agent":"`+agent+`"`); n != 1 {
			t.Errorf("session.start for the %s arrived %d times, want 1", agent, n)
		}
		if n := countWith(evs, "session.end", `"agent":"`+agent+`"`); n != 1 {
			t.Errorf("session.end for the %s arrived %d times, want 1", agent, n)
		}
	}
	// One check result follows Build and one follows Test and Review.
	if n := countOf(got, "run-status/check.done"); n != 2 {
		t.Errorf("check.done arrived %d times, want 2", n)
	}
	// No live subscriber is reachable through the binary: plan streams only run-start, error and plan events, and ui streams
	// state snapshots, not events. So the store, which the bus appends to before it broadcasts, is the record checked.
}

// countWith counts the events of the subtype whose data contains want.
func countWith(evs []event, sub, want string) int {
	n := 0
	for _, e := range evs {
		if e.Subtype == sub && strings.Contains(string(e.Data), want) {
			n++
		}
	}
	return n
}

// containers lists the fake containers left in the fake docker's folder.
func containers(dir string) []string {
	var out []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// Full run: install, plan on the fixture's goal, run the plan id; the group passes build, test and review, lands on the epic of
// the bare remote with 1 pull request, and cleanup leaves 0 worktrees, 0 containers and no lease.
func TestFullRunInstallPlanRunPublishesAndCleansUp(t *testing.T) {
	r := newFixtureRig(t, false, nil)
	boxes, made := r.writeLiveDocker()
	if res := r.terminal(60*time.Second, "install", "--hosts", "claude", "--no-path"); res.code != 0 {
		t.Fatalf("komodo install exited %d\n%s", res.code, res.stdout)
	}
	id, _ := r.planGoal("add Sub and Mul to the calc package")
	r.mustRun("run", id)
	evs := r.waitDone(90 * time.Second)
	if res := r.komodo(60*time.Second, "run", id); res.code != 0 && !strings.Contains(res.stdout, "done") {
		t.Errorf("komodo run %s after the group finished exited %d\n%s", id, res.code, res.stdout)
	}
	if miss, ok := subseq(stages(evs), []string{"build", "test_review", "commit", "cleanup"}); !ok {
		t.Fatalf("stage %s missing: %v", miss, stages(evs))
	}
	if !has(evs, "run-status", "group.done", "complete") {
		t.Fatalf("the group did not end complete\n%s", r.evidence(evs))
	}
	for _, f := range []string{"calc/sub.go", "calc/mul.go", "calc/sub_test.go", "calc/mul_test.go"} {
		if out := r.git(r.origin, "show", "komodo/"+id+":"+f); out == "" {
			t.Errorf("komodo/%s of the remote lacks %s", id, f)
		}
	}
	if n := strings.Count(r.ghCalls(), "pr create"); n != 1 {
		t.Errorf("gh opened %d pull requests, want 1: %q", n, r.ghCalls())
	}
	if wt := r.worktrees(); len(wt) != 0 {
		t.Errorf("worktrees remain: %v", wt)
	}
	if b, _ := os.ReadFile(made); strings.Count(string(b), "create ") != 2 {
		// Quality Assurance and the recheck after landing each run in a fresh sandbox instance, destroyed after.
		t.Errorf("the run made containers %q, want exactly 2", b)
	}
	if c := containers(boxes); len(c) != 0 {
		t.Errorf("containers remain: %v", c)
	}
	if ls := r.leaseFiles(); len(ls) != 0 {
		t.Errorf("leases remain: %v", ls)
	}
}

// runIDv7 matches a version 7 run identifier.
var runIDv7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Full run, second step: `komodo run <plan id>` twice in a row meets the run the first command started; the second must
// continue or be refused as already running, never fail on a git error.
func TestRunTwiceIsRefusedCleanly(t *testing.T) {
	r := newFixtureRig(t, false, nil)
	id, _ := r.planGoal("add Sub and Mul to the calc package")
	r.mustRun("run", id)
	res := r.komodo(60*time.Second, "run", id)
	if strings.Contains(res.stdout, "fatal:") {
		t.Fatalf("a second run of %s failed with a raw git error\n%s", id, res.stdout)
	}
	if res.code != 0 && !strings.Contains(res.stdout, "running") {
		t.Fatalf("a second run of %s exited %d\n%s", id, res.code, res.stdout)
	}
}

// countOf counts the entries equal to s.
func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}
