//go:build e2e && !windows

package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// envelope decodes the JSON reply of one command.
func envelope(t *testing.T, res result) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &m); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s\n%s", err, res.stdout, res.stderr)
	}
	return m
}

// dataOf returns the data object of a reply.
func dataOf(t *testing.T, res result) map[string]any {
	d, _ := envelope(t, res)["data"].(map[string]any)
	return d
}

// mustRun runs a komodo command and fails the test on a non-zero exit.
func (r *binRig) mustRun(args ...string) result {
	r.t.Helper()
	res := r.komodo(60*time.Second, args...)
	if res.code != 0 {
		r.t.Fatalf("komodo %s exited %d\n%s\n%s", strings.Join(args, " "), res.code, res.stdout, res.stderr)
	}
	return res
}

// setConfig writes one setting into the rig's home as a person's config set would.
func (r *binRig) setConfig(key, old, val string) {
	r.t.Helper()
	cfg, err := config.New(
		r.home,
		config.Known{GuardRows: task.KnownRows(), Tools: task.KnownTools()},
		func(string) bool { return true },
	)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := cfg.SetConfigItem(config.Change{Key: key, Old: old, New: val}); err != nil {
		r.t.Fatalf("set %s: %v", key, err)
	}
}

// gateSecondTask makes the fixture builder wait before its second task; reached closes when it waits.
func (r *binRig) gateSecondTask() (reached <-chan struct{}, release func()) {
	hit, hold := make(chan struct{}), make(chan struct{})
	var once, rel sync.Once
	r.model.builder = fixtureBuilderGated(false, func(string) {
		once.Do(func() { close(hit) })
		select {
		case <-hold:
		case <-time.After(90 * time.Second):
		}
	})
	return hit, func() { rel.Do(func() { close(hold) }) }
}

// waitChan waits for c or fails the test.
func (r *binRig) waitChan(c <-chan struct{}, what string) {
	r.t.Helper()
	select {
	case <-c:
	case <-time.After(60 * time.Second):
		r.t.Fatalf("%s never happened\n%s", what, r.evidence(r.events()))
	}
}

// enginePID reads the engine's process id from its heartbeat file.
func (r *binRig) enginePID() int {
	b, err := os.ReadFile(filepath.Join(r.home, "state", "engine.beat"))
	if err != nil {
		return 0
	}
	var beat struct {
		PID int `json:"pid"`
	}
	_ = json.Unmarshal(b, &beat)
	return beat.PID
}

// eventFiles returns every event log file with its content.
func (r *binRig) eventFiles() map[string]string {
	out := map[string]string{}
	dirs := []string{filepath.Join(r.home, "events")}
	if b, err := os.ReadFile(filepath.Join(r.home, "events", "runs.json")); err == nil {
		var m map[string]string
		_ = json.Unmarshal(b, &m)
		for _, d := range m {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.jsonl"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			out[f] = string(b)
		}
	}
	return out
}

// groupDone reports whether the group with the id has a group.done event.
func groupDone(evs []event, id string) bool {
	for _, e := range evs {
		if e.Group == id && e.Subtype == "group.done" {
			return true
		}
	}
	return false
}

// Rework (fixture B): the builder breaks Add; Quality Assurance or Reviewer returns the group to the Builder, and after the bound
// it ends Blocked with reason regression.
func TestReworkBrokenAddEndsBlockedRegression(t *testing.T) {
	// Fixture B: the group may touch calc.go, so the builder's change to Add passes the scope guard and breaks TestAdd.
	r := newCalcRig(t, func(b *intake.Blueprint) {
		g := &b.Groups[0]
		g.Files = append(g.Files, "calc/calc.go")

		for i := range g.Tasks {
			g.Tasks[i].Files = append(g.Tasks[i].Files, "calc/calc.go")
		}
	})
	r.model.builder = fixtureBuilder(true)
	r.mustRun("run", fixturePlan)
	var evs []event
	for end := time.Now().Add(150 * time.Second); ; time.Sleep(time.Second) {
		evs = r.events()
		if has(evs, "run-failure", "group.blocked", "") {
			break
		}
		if has(evs, "run-status", "group.done", "") {
			if strings.Contains(r.git(r.origin, "show", "komodo/"+fixtureEpic+":calc/calc.go"), "a - b") {
				t.Fatalf("production bug: the broken Add landed on the epic branch and the group ended complete; "+
					"check.done reported baseline pass and regression false (internal/agent/checks.go:79-83)\n%s",
					r.evidence(evs))
			}
			t.Fatalf("the group ended complete without entering the rework loop, and the broken Add never reached "+
				"the epic branch (calc.go still adds): fixture B's change did not land\n%s", r.evidence(evs))
		}
		if time.Now().After(end) {
			t.Fatalf("the group neither blocked nor finished\n%s", r.evidence(evs))
		}
	}
	if !has(evs, "run-failure", "group.blocked", "regression") {
		t.Fatalf("the group blocked without reason regression\n%s", r.evidence(evs))
	}
	// The event log holds every round: 3 builds (3 tries total), a return after each failed try but the last, and a
	// check result per try.
	builds, returns, checks := 0, 0, 0
	for _, e := range evs {
		switch {
		case e.Type == "run-status" && e.Subtype == "session.start" && strings.Contains(string(e.Data), `"agent":"builder"`):
			builds++
		case e.Type == "run-status" && e.Subtype == "stage.enter" && strings.Contains(string(e.Data), `"return":true`):
			returns++
		case e.Type == "run-status" && e.Subtype == "check.done":
			checks++
		}
	}
	if builds != 3 || returns != 2 || checks != 3 {
		t.Fatalf("the log holds %d builder sessions, %d returns, %d check results; want 3, 2, 3\n%s",
			builds, returns, checks, r.evidence(evs))
	}
}

// Old failure (fixture A): a test that already fails at the base commit lets the group pass with a warning naming it.
func TestOldFailurePassesWithAWarningNamingTheTest(t *testing.T) {
	r := newFixtureRig(t, true, map[string]string{
		"calc/old_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAlreadyBroken(t *testing.T) { t.Fatal(\"broken at base\") }\n",
	})
	r.model.qaExit = 1 // the old failure makes go test ./... exit 1, and Quality Assurance reports that exit as evidence
	r.mustRun("run", fixturePlan)
	evs := r.waitDone(120 * time.Second)
	if !has(evs, "run-status", "group.done", "complete") {
		t.Fatalf("the group did not pass\n%s", r.evidence(evs))
	}
	warned := false
	for _, e := range evs {
		if strings.Contains(string(e.Data), "TestAlreadyBroken") && strings.Contains(string(e.Data), "warn") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning names TestAlreadyBroken\n%s", r.evidence(evs))
	}
}

// Race: two `komodo run <plan id> tg-1` at once get 1 lease; the other is refused and only 1 push reaches the remote.
func TestRaceTwoRunsOneLeaseOnePush(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	reached, release := r.gateSecondTask()
	t.Cleanup(release)
	// No engine runs yet, so both commands start one at the same moment and each must reach the engine that wins.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	outs := make([]string, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(r.bin, "run", fixturePlan, fixtureGroup)
			cmd.Dir, cmd.Env = r.repo, r.env
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			if ee, ok := err.(*exec.ExitError); ok {
				codes[i] = ee.ExitCode()
			}
			outs[i] = out.String()
		}()
	}
	wg.Wait()
	// While the winner's builder waits, the group holds exactly 1 lease, and it names the run of only one of the commands.
	r.waitChan(reached, "the builder's second task")
	leases := r.leaseFiles()
	if len(leases) != 1 {
		t.Fatalf("%d leases granted, want 1: %v\n%v", len(leases), leases, outs)
	}
	rec, _ := os.ReadFile(leases[0])
	holders := 0
	for _, out := range outs {
		var reply struct {
			Data struct {
				RunID string `json:"run_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.Data.RunID == "" {
			t.Fatalf("a command's reply holds no run id: %v\n%s", err, out)
		}
		if bytes.Contains(rec, []byte(reply.Data.RunID)) {
			holders++
		}
	}
	if holders != 1 {
		t.Fatalf("the lease names the run of %d commands, want 1\n%s\n%v", holders, rec, outs)
	}
	release()
	// The epic branch is pushed and the pull request opened once the epic goes quiet, after the group is done.
	r.waitFor(60*time.Second, "run-complete", "done")
	evs := r.events()
	for end := time.Now().Add(10 * time.Second); countOf(keys(evs), "run-status/request.refused") < 1; {
		time.Sleep(200 * time.Millisecond)
		if time.Now().After(end) {
			t.Fatalf("neither command was refused: codes %v\n%v\n%s", codes, outs, r.evidence(evs))
		}
		evs = r.events()
	}
	// The command that lost is refused because the group is already running, not for another reason.
	if n := countWith(evs, "request.refused", "the group is running"); n != 1 {
		t.Errorf("%d refusals say the group is running, want 1\n%s", n, r.evidence(evs))
	}
	if n := countOf(keys(evs), "run-status/request.refused"); n != 1 {
		t.Errorf("%d commands were refused, want 1", n)
	}
	if n := countOf(keys(evs), "run-status/run.start"); n != 1 {
		t.Errorf("%d runs started, want 1\n%v", n, outs)
	}
	if n := countOf(keys(evs), "run-status/publish.done"); n != 1 {
		t.Errorf("%d pushes, want 1", n)
	}
	if n := strings.Count(r.ghCalls(), "pr create"); n != 1 {
		t.Errorf("%d pull requests, want 1", n)
	}
}

// Stop: stopping during Build after the first task keeps that task as a local commit, pushes nothing, revokes the lease, and
// a later run continues the group.
func TestStopDuringBuildKeepsTheFinishedTaskAndContinues(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	reached, release := r.gateSecondTask()
	t.Cleanup(release)
	r.mustRun("run", fixturePlan, fixtureGroup)
	r.waitChan(reached, "the builder's second task")
	r.mustRun("stop", fixturePlan, fixtureGroup)
	r.waitFor(60*time.Second, "run-complete", "stopped")
	wts := r.worktrees()
	if len(wts) != 1 {
		t.Fatalf("want the group's one worktree after the stop, got %v", wts)
	}
	if log := r.git(wts[0], "log", "--format=%s"); !strings.Contains(log, "tsk-") {
		t.Fatalf("the finished task is not a local commit in the group's worktree: %q", log)
	}
	if out := r.git(r.origin, "branch", "--list", "komodo/*"); strings.Contains(out, fixtureGroup) {
		t.Fatalf("the group branch reached the remote: %q", out)
	}
	if out := r.git(r.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "sub.go") {
		t.Fatalf("work reached the remote's main")
	}
	if strings.Contains(r.git(r.origin, "for-each-ref", "--format=%(refname)"), "komodo/"+fixtureEpic) {
		if out := r.git(r.origin, "ls-tree", "-r", "--name-only", "komodo/"+fixtureEpic); strings.Contains(out, "sub.go") {
			t.Fatalf("the finished task was pushed to the epic branch")
		}
	}
	for _, l := range r.leaseFiles() {
		if b, _ := os.ReadFile(l); !bytes.Contains(b, []byte(`"revoked"`)) {
			t.Fatalf("the lease was not revoked: %s\n%s", l, b)
		}
	}
	release()
	r.mustRun("run", fixturePlan, fixtureGroup)
	evs := r.waitDone(120 * time.Second)
	if !has(evs, "run-status", "group.done", "complete") {
		t.Fatalf("the continued group did not finish\n%s", r.evidence(evs))
	}
}

// Crash: kill -9 the engine during Build; a later run continues from the saved state, and every event line written before
// the kill is still in the log.
func TestCrashDuringBuildContinuesFromSavedState(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	reached, release := r.gateSecondTask()
	t.Cleanup(release)
	r.mustRun("run", fixturePlan, fixtureGroup)
	r.waitChan(reached, "the builder's second task")
	time.Sleep(500 * time.Millisecond)
	before := r.eventFiles()
	r.killEngine()
	time.Sleep(time.Second)
	release()
	// Recovery reads only the saved state: an unreadable event log must not stop the group.
	for f := range before {
		if filepath.Base(f) != "engine.jsonl" {
			_ = os.Chmod(f, 0o000)
			defer os.Chmod(f, 0o644)
		}
	}
	r.mustRun("run", fixturePlan, fixtureGroup)
	for f := range before {
		_ = os.Chmod(f, 0o644)
	}
	evs := r.waitDone(120 * time.Second)
	if !has(evs, "run-status", "task.done", "tsk-sub") {
		t.Fatalf("the first task was lost\n%s", r.evidence(evs))
	}
	after := r.eventFiles()
	for f, body := range before {
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			if line != "" && !strings.Contains(after[f], line) {
				t.Fatalf("event line lost from %s after the kill: %s", f, line)
			}
		}
	}
}

// Fresh machine: on a clean macOS machine, following only README.md publishes the fixture group.
func TestFreshMachineFollowsReadmeOnly(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot(), "README.md")); err != nil {
		t.Skip("blocked: the repository has no README.md to follow, and the check needs a clean macOS machine")
	}
	t.Skip("blocked: needs a clean macOS machine and a person following README.md; not automatable here")
}

// Engine and socket: status answers over engine.sock from the engine running the run, which exits once no run is live.
func TestEngineServesStatusAndExitsWhenIdle(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	reached, release := r.gateSecondTask()
	t.Cleanup(release)
	res := r.mustRun("run", fixturePlan, fixtureGroup)
	runID, _ := dataOf(t, res)["run_id"].(string)
	r.waitChan(reached, "the builder's second task")
	pid := r.enginePID()
	st := r.mustRun("status")
	if !strings.Contains(st.stdout, runID) {
		t.Fatalf("status does not list the live run %s\n%s", runID, st.stdout)
	}
	if _, err := os.Stat(filepath.Join(r.home, "state", "engine.sock")); err != nil {
		t.Fatalf("no engine.sock: %v", err)
	}
	if p := r.enginePID(); p != pid || p == 0 {
		t.Fatalf("status was answered by engine %d, the run is on %d", p, pid)
	}
	release()
	r.waitFor(120*time.Second, "run-complete", "done")
	for end := time.Now().Add(120 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(time.Second) {
		if time.Now().After(end) {
			t.Fatalf("engine %d still runs 120s after the last run ended", pid)
		}
	}
}

// Event log: replay of a finished run with a torn last line lists every event, drops the torn line, marks the gap and
// makes 0 model calls.
func TestReplayDropsATornLineAndCallsNoModel(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	res := r.mustRun("run", fixturePlan)
	runID, _ := dataOf(t, res)["run_id"].(string)
	r.waitFor(90*time.Second, "run-complete", "done")
	var log string
	for f := range r.eventFiles() {
		if strings.Contains(f, runID) {
			log = f
		}
	}
	if log == "" {
		t.Fatalf("no event log for run %s in %v", runID, r.eventFiles())
	}
	body, _ := os.ReadFile(log)
	lines := strings.Count(string(body), "\n")
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"run-status","subtype":"stage.en`)
	_ = f.Close()
	calls := r.model.callCounts()
	if lab := r.komodo(60*time.Second, "replay", fixturePlan, fixtureGroup); lab.code != 0 {
		t.Errorf("replay of the finished group label exited %d\n%s", lab.code, lab.stdout)
	}
	rep := r.mustRun("replay", runID)
	d := dataOf(t, rep)
	steps, _ := d["steps"].([]any)
	gaps, _ := d["gaps"].(float64)
	if gaps < 1 {
		t.Errorf("replay marked %v gaps, want at least 1", gaps)
	}
	if len(steps) != lines {
		t.Errorf("replay listed %d events; the log holds %d whole lines, and replay must list every one", len(steps), lines)
	}
	if got := r.model.callCounts(); len(got) != len(calls) || got["builder"] != calls["builder"] ||
		got["qa"] != calls["qa"] || got["reviewer"] != calls["reviewer"] {
		t.Errorf("replay called the model: %v then %v", calls, got)
	}
}

// Final error event: an error the Error Handler catches ends the process with an error event marked final, and the exit
// code matches the fault's kind.
func TestFinalErrorEventAndExitCode(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	// Seed: an engine whose home makes the control socket path longer than the system allows cannot bind, which ends
	// the engine on an internal fault.
	home := filepath.Join(r.root, strings.Repeat("x", 120))
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(r.bin, "internal", "engine")
	cmd.Dir = r.repo
	for _, kv := range r.env {
		if !strings.HasPrefix(kv, "KOMODO_HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "KOMODO_HOME="+home)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	body, err := os.ReadFile(filepath.Join(home, "events", "engine.jsonl"))
	if err != nil {
		t.Fatalf("the engine wrote no event log: %v\n%s", err, out.String())
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	var last struct {
		Type string `json:"type"`
		Data struct {
			Kind  string `json:"kind"`
			Final bool   `json:"final"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("the last event line is not JSON: %v\n%s", err, body)
	}
	if last.Type != "error" || !last.Data.Final {
		t.Fatalf("the process's last event is not a final error event: %s", lines[len(lines)-1])
	}
	want := map[string]int{"refused": 1, "usage": 2, "blocked": 3, "internal": 4}[last.Data.Kind]
	if want == 0 || code != want {
		t.Fatalf("a %q fault exited %d, want %d\n%s", last.Data.Kind, code, want, out.String())
	}
}

// Answering a block: a builder asking for a file outside its group blocks; status --wait returns the question, --approve
// continues with the file and --deny refuses it.
func TestAnsweringABlockApproveAndDeny(t *testing.T) {
	for _, answer := range []string{"approve", "deny"} {
		t.Run(answer, func(t *testing.T) {
			r := newFixtureRig(t, true, nil)
			// The builder first ends with needs_scope for calc/outside.go; once answered it writes the file (if
			// granted) and does the fixture's tasks.
			inner := fixtureBuilder(false)
			var mu sync.Mutex
			asked, offset := false, -1
			r.model.builderFinal = func() string {
				mu.Lock()
				defer mu.Unlock()
				if asked {
					return ""
				}
				asked = true
				return `{"status":"needs_scope","summary":"needs a helper file","paths":["calc/outside.go"],` +
					`"reason":"the helper lives outside the group's files"}`
			}
			r.model.builder = func(brief string, turn int) (string, map[string]any) {
				mu.Lock()
				defer mu.Unlock()
				if !asked {
					return "", nil
				}
				if offset < 0 {
					offset = turn
					if answer == "approve" {
						return "write", map[string]any{"path": "calc/outside.go", "content": "package calc\n"}
					}
				}
				if answer == "approve" {
					return inner(brief, turn-offset-1)
				}
				return inner(brief, turn-offset)
			}
			r.mustRun("run", fixturePlan, fixtureGroup)
			st := r.komodo(120*time.Second, "status", "--wait", "--timeout", "90s")
			if st.code != 3 || !strings.Contains(st.stdout, "outside.go") {
				t.Fatalf("status --wait did not return the question (exit %d): %s\n%s", st.code, st.stdout, r.evidence(r.events()))
			}
			r.mustRun("run", fixturePlan, fixtureGroup, "--"+answer)
			r.waitFor(120*time.Second, "run-status", "group.done")
			// The epic branch reaches origin only after the epic goes quiet.
			evs := r.waitFor(60*time.Second, "run-complete", "done")
			ls := r.git(r.origin, "ls-tree", "-r", "--name-only", "komodo/"+fixtureEpic)
			if answer == "approve" && !strings.Contains(ls, "calc/outside.go") {
				t.Fatalf("--approve did not continue with the file\n%s", r.evidence(evs))
			}
			if answer == "deny" && strings.Contains(ls, "calc/outside.go") {
				t.Fatalf("--deny let the file land")
			}
		})
	}
}

// Scheduled run: schedule.json holds target, at, queued_at and start; status lists the run; stop <plan id> removes it.
func TestScheduledRunIsQueuedListedAndStopped(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res := r.mustRun("run", fixturePlan, "--schedule", at)
	runID, _ := dataOf(t, res)["run_id"].(string)
	b, err := os.ReadFile(filepath.Join(r.home, "state", "schedule.json"))
	if err != nil {
		t.Fatalf("no schedule.json: %v", err)
	}
	for _, k := range []string{`"target"`, `"at"`, `"queued_at"`, `"start"`} {
		if !bytes.Contains(b, []byte(k)) {
			t.Errorf("schedule.json lacks %s:\n%s", k, b)
		}
	}
	if st := r.mustRun("status"); !strings.Contains(st.stdout, runID) || runID == "" {
		t.Errorf("status does not list the queued run %q\n%s", runID, st.stdout)
	}
	r.mustRun("stop", fixturePlan)
	b, _ = os.ReadFile(filepath.Join(r.home, "state", "schedule.json"))
	if bytes.Contains(b, []byte(`"`+fixturePlan+`"`)) {
		t.Fatalf("stop %s left the queued run:\n%s", fixturePlan, b)
	}
}

// Overdue run: a queued run whose time passed while the engine was down starts at once when the next command starts the
// engine.
func TestOverdueRunStartsWhenTheEngineStarts(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	at := time.Now().Add(4 * time.Second).UTC().Format(time.RFC3339)
	r.mustRun("run", fixturePlan, "--schedule", at)
	r.killEngine()
	time.Sleep(6 * time.Second)
	r.mustRun("status")
	evs := r.waitDone(120 * time.Second)
	if !has(evs, "run-status", "group.done", "complete") {
		t.Fatalf("the overdue run did not run\n%s", r.evidence(evs))
	}
}

// Labels and run ids: two runs of the plan get their own version 7 identifiers, replies show label and run id, and replay uses
// the latest attempt.
func TestLabelsAndRunIDsReplayUsesLatest(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	reached, release := r.gateSecondTask()
	first := r.mustRun("run", fixturePlan)
	r.waitChan(reached, "the builder's second task")
	r.mustRun("stop", fixturePlan)
	r.waitFor(60*time.Second, "run-complete", "stopped")
	release()
	second := r.mustRun("run", fixturePlan)
	r.waitDone(120 * time.Second)
	ids := []string{}
	for _, res := range []result{first, second} {
		id, _ := dataOf(t, res)["run_id"].(string)
		if !runIDv7.MatchString(id) || !strings.Contains(res.stdout, fixturePlan) || !strings.Contains(res.stdout, id) {
			t.Fatalf("reply lacks the label or a version 7 run id: %q\n%s", id, res.stdout)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Fatalf("both runs share id %s", ids[0])
	}
	rep := r.komodo(60*time.Second, "replay", fixturePlan, fixtureGroup)
	if rep.code != 0 {
		t.Fatalf("replay %s %s exited %d\n%s", fixturePlan, fixtureGroup, rep.code, rep.stdout)
	}
	if got, _ := dataOf(t, rep)["run_id"].(string); got != ids[1] {
		t.Fatalf("replay used run %s, want the latest %s", got, ids[1])
	}
}

// Whole plan: run <plan id> runs every group in dependency order, and a second run starts no group that is done.
func TestRunPlanRunsGroupsInDependencyOrderAndSkipsDone(t *testing.T) {
	group := func(name, id, file string, deps ...string) intake.Group {
		return intake.Group{
			ID: id, Name: name, Goal: "Add " + file, Points: 1, Subsystem: "none", Priority: intake.PriorityNormal,
			Files: []string{file}, Dependencies: deps,
			Tasks: []intake.Task{{
				Name: "tsk-" + strings.TrimSuffix(filepath.Base(file), ".go"), Files: []string{file},
				DoneWhen: "go build ./...", EstimateLines: 5, Card: "Add the function.",
			}},
		}
	}
	r := newCalcRig(t, func(b *intake.Blueprint) {
		b.Groups = append(b.Groups,
			group("tg-2", "0192e4c5-7a10-7d3e-9b2f-000000000401", "calc/alpha.go"),
			group("tg-3", "0192e4c5-7a10-7d3e-9b2f-000000000402", "calc/beta.go", "tg-2"),
		)
	})
	r.model.builder = fixtureBuilder(false)
	r.mustRun("run", fixturePlan)
	evs := r.waitFor(180*time.Second, "run-complete", "done")
	for end := time.Now().Add(120 * time.Second); countOf(keys(evs), "run-status/group.done") < 3; time.Sleep(time.Second) {
		if time.Now().After(end) {
			t.Fatalf("the groups did not all finish\n%s", r.evidence(evs))
		}
		evs = r.events()
	}
	starts := countOf(keys(evs), "run-status/session.start")
	r.mustRun("run", fixturePlan)
	time.Sleep(10 * time.Second)
	if got := countOf(keys(r.events()), "run-status/session.start"); got != starts {
		t.Fatalf("a second run started %d more sessions for groups that were done", got-starts)
	}
	firstDone, secondStart := -1, -1
	for i, e := range evs {
		if e.Group == "0192e4c5-7a10-7d3e-9b2f-000000000401" && e.Subtype == "group.done" && firstDone < 0 {
			firstDone = i
		}
		if e.Group == "0192e4c5-7a10-7d3e-9b2f-000000000402" && e.Subtype == "session.start" && secondStart < 0 {
			secondStart = i
		}
	}
	if firstDone < 0 || secondStart < firstDone {
		t.Fatalf("tg-3 started (%d) before tg-2 finished (%d)\n%s", secondStart, firstDone, r.evidence(evs))
	}
}

// Sandbox: without Docker Quality Assurance uses the operating-system sandbox and the run report says so; with Docker the
// validation runs in a per-group container that cleanup destroys.
func TestSandboxFallsBackWithoutDocker(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	r.env = append(r.env, noDocker)
	r.mustRun("run", fixturePlan)
	evs := r.waitDone(120 * time.Second)
	if !has(evs, "run-status", "sandbox.fallback", "") {
		t.Fatalf("a run without Docker published no sandbox.fallback event\n%s", r.evidence(evs))
	}
	t.Skip("blocked: the Docker half needs a real container runtime; the fake docker on PATH cannot run the validation")
}

// Free checks: a failing completion command returns to the Builder with its exact output, and Quality Assurance and Reviewer make
// 0 model calls.
func TestFreeCheckFailureReturnsOutputToBuilder(t *testing.T) {
	r := newCalcRig(t, func(b *intake.Blueprint) {
		b.Groups[0].Tasks[0].DoneWhen = "echo FREE-CHECK-OUTPUT-42 && false"
	})
	r.model.builder = fixtureBuilder(false)
	r.mustRun("run", fixturePlan)
	for end := time.Now().Add(90 * time.Second); ; time.Sleep(time.Second) {
		r.model.mu.Lock()
		seen := false
		for _, b := range r.model.bodies {
			if strings.Contains(b, "FREE-CHECK-OUTPUT-42") && strings.Contains(b, "tool_result") {
				seen = true
			}
		}
		r.model.mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the builder never saw the check's output\n%s", r.evidence(r.events()))
		}
	}
	time.Sleep(3 * time.Second)
	if c := r.model.callCounts(); c["qa"] != 0 || c["reviewer"] != 0 {
		t.Fatalf("Quality Assurance or reviewer was called after a failed free check: %v", c)
	}
}

// Cache: an identical request takes its context pieces from the cache, and still makes a new model call.
func TestCacheServesContextAndStillCallsTheModel(t *testing.T) {
	r := newFixtureRig(t, false, nil)
	// A write replaces the file through a rename, so its inode changes; a read keeps the inode and stamps the modified time.
	cacheFiles := func() map[string]os.FileInfo {
		out := map[string]os.FileInfo{}
		_ = filepath.Walk(filepath.Join(r.home, "cache"), func(p string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() {
				out[p] = fi
			}
			return nil
		})
		return out
	}
	goal := "add Sub and Mul to the calc package"
	r.mustRun("plan", goal)
	// The first plan fills the cache, so the identical requests are the second and third plans.
	_ = r.komodo(60*time.Second, "plan", goal)
	time.Sleep(5 * time.Second)
	first, calls := cacheFiles(), r.model.callCounts()["planner"]
	if len(first) == 0 || calls == 0 {
		t.Fatalf("the first plan cached nothing (%d files) or called no planner (%d)", len(first), calls)
	}
	time.Sleep(1100 * time.Millisecond)
	_ = r.komodo(60*time.Second, "plan", goal)
	time.Sleep(5 * time.Second)
	if got := r.model.callCounts()["planner"]; got <= calls {
		t.Fatalf("the repeated request made no new model call: %d then %d", calls, got)
	}
	after := cacheFiles()
	read := 0
	for p, old := range first {
		now, ok := after[p]
		switch {
		case !ok:
			t.Errorf("the repeated request removed cache entry %s", p)
		case !os.SameFile(old, now):
			t.Errorf("the repeated request rewrote cache entry %s instead of reading it", p)
		case now.ModTime().After(old.ModTime()):
			read++
		}
	}
	for p := range after {
		if _, ok := first[p]; !ok {
			t.Errorf("the repeated request wrote a new cache entry %s", p)
		}
	}
	if read == 0 {
		t.Fatalf("the repeated request read none of the %d cache entries", len(first))
	}
}

// recheckFlagRef is the git reference the test creates to let the recheck pass; the check reads it through git, which every
// sandbox backend exposes read-only.
const recheckFlagRef = "refs/komodo-test/recheck-ok"

// Epic landing: an epic of 2 groups lands them one at a time; a failing recheck after the first landing pauses the epic, the
// second group lands only after a recheck passes, and 1 pull request opens after the last landing.
func TestEpicLandingPausesOnRecheckAndLandsInTurn(t *testing.T) {
	r := newCalcRig(t, func(b *intake.Blueprint) {
		b.Groups[0].Tasks[1].DoneWhen = "go test ./calc/ -run TestMul && { ! git cat-file -e komodo/" + fixtureEpic +
			":calc/mul.go || git rev-parse -q --verify " + recheckFlagRef + "; }"
		b.Groups = append(b.Groups, intake.Group{
			ID: "0192e4c5-7a10-7d3e-9b2f-000000000302", Name: "tg-2", Goal: "Add gamma", Points: 1, Subsystem: "none",
			Priority: intake.PriorityNormal, Files: []string{"calc/gamma.go"}, Dependencies: []string{"tg-1"},
			Tasks: []intake.Task{{
				Name: "tsk-gamma", Files: []string{"calc/gamma.go"}, DoneWhen: "go build ./...", EstimateLines: 5,
				Card: "Add the function.",
			}},
		})
	})
	r.model.builder = fixtureBuilder(false)
	r.mustRun("run", fixturePlan)
	evs := r.waitFor(150*time.Second, "run-status", "epic.paused")
	// Nothing is pushed while the epic is paused, so the epic branch exists only in the local repository.
	if ls := r.git(r.repo, "ls-tree", "-r", "--name-only", "komodo/"+fixtureEpic); strings.Contains(ls, "gamma.go") {
		t.Fatalf("the second group landed while the epic was paused")
	}
	if out := r.git(r.origin, "branch", "--list", "komodo/"+fixtureEpic); strings.TrimSpace(out) != "" {
		t.Fatalf("the epic branch reached the remote while the epic was paused: %q", out)
	}
	// originTree lists the epic branch on the remote; it is empty until the epic is pushed.
	originTree := func() string {
		if strings.TrimSpace(r.git(r.origin, "branch", "--list", "komodo/"+fixtureEpic)) == "" {
			return ""
		}
		return r.git(r.origin, "ls-tree", "-r", "--name-only", "komodo/"+fixtureEpic)
	}
	if strings.Contains(r.ghCalls(), "pr create") {
		t.Fatalf("a pull request opened before the last landing")
	}
	r.git(r.repo, "update-ref", recheckFlagRef, "HEAD")
	// A paused epic admits only the group that paused it; its next landing rechecks the epic branch.
	r.mustRun("run", fixturePlan, fixtureGroup)
	r.waitFor(150*time.Second, "run-status", "epic.resumed")
	time.Sleep(3 * time.Second)
	if !groupDone(r.events(), groupID) {
		t.Fatalf("no group.done after the recheck passed and tg-1.1 landed\n%s", r.evidence(r.events()))
	}
	if !strings.Contains(originTree(), "gamma.go") {
		_ = r.komodo(60*time.Second, "run", fixturePlan)
	}
	for end := time.Now().Add(150 * time.Second); ; time.Sleep(time.Second) {
		if strings.Contains(originTree(), "gamma.go") && strings.Contains(r.ghCalls(), "pr create") {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the second group did not land after the recheck passed\n%s", r.evidence(r.events()))
		}
	}
	_ = evs
	if n := strings.Count(r.ghCalls(), "pr create"); n != 1 {
		t.Fatalf("%d pull requests, want 1", n)
	}
}

// Limits: a group at 90% of its token cap gets a nudge and pauses with cause group_tokens at 100%; --approve continues it.
func TestLimitsGroupTokenCapNudgesPausesAndApproveContinues(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	r.setConfig("limits.group_tokens", "", "150")
	r.mustRun("run", fixturePlan, fixtureGroup)
	var evs []event
	for end := time.Now().Add(120 * time.Second); ; time.Sleep(time.Second) {
		evs = r.events()
		if has(evs, "run-status", "group.paused", "") {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the group neither paused nor finished\n%s", r.evidence(evs))
		}
	}
	// group.paused carries only until and link; the cause is the group_tokens error the Resource Manager threw first.
	if !has(evs, "error", "fault.resource", "group_tokens") {
		t.Fatalf("the group paused without cause group_tokens\n%s", r.evidence(evs))
	}
	// The nudge changes no state and publishes no event, so it is not checked here.
	r.mustRun("run", fixturePlan, fixtureGroup, "--approve")
	r.waitFor(120*time.Second, "run-status", "group.resumed")
}

// Limits, plan-window half: a fake provider reports a plan window at 80%, then 100%; the harness warns at 80% and pauses the
// affected groups at 100%.
func TestLimitsPlanWindowWarnsAndPauses(t *testing.T) {
	t.Skip("blocked: needs a fake subscription link reporting 80% and 100% windows; the rig has only the paid Messages " +
		"interface, and its claude program is logged out")
}

// Model links: with paid use off and the Claude window spent, the subscription link is tried first, the paid interface is
// refused, and the groups pause.
func TestModelLinksPaidOffPausesInsteadOfPaying(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	r.setConfig("providers.paid", "allowed", "never")
	r.mustRun("run", fixturePlan, fixtureGroup)
	var evs []event
	for end := time.Now().Add(90 * time.Second); ; time.Sleep(time.Second) {
		evs = r.events()
		if has(evs, "run-status", "group.paused", "") || has(evs, "run-failure", "group.blocked", "") {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the group neither paused nor blocked\n%s", r.evidence(evs))
		}
	}
	if c := r.model.callCounts(); c["builder"]+c["qa"]+c["reviewer"] != 0 {
		t.Fatalf("the paid interface was called with paid use off: %v", c)
	}
	if has(evs, "run-failure", "group.blocked", "paid_refused") {
		t.Skip("blocked: the paid interface is refused (0 paid calls, block paid_refused), but the spent-window " +
			"pause needs a fake claude program reporting a full window; the rig's claude is logged out")
	}
	if !has(evs, "run-status", "provider.skip", "claude-cli") {
		t.Errorf("the subscription link was not tried first\n%s", r.evidence(evs))
	}
}

// Tool servers: with none switched on, no tool server starts and no mcp event is published.
func TestToolServersNoneSwitchedOnStartsNone(t *testing.T) {
	r := newFixtureRig(t, true, nil)
	r.mustRun("run", fixturePlan)
	evs := r.waitDone(120 * time.Second)
	for _, e := range evs {
		if e.Type == "mcp" {
			t.Fatalf("a tool server event with none switched on: %s %s", e.Subtype, e.Data)
		}
	}
}

// Tool servers, added-server half: a server added for a named agent runs inside the sandbox with no network, and each call is
// an event.
func TestToolServersAddedServerRunsSandboxedWithAnEventPerCall(t *testing.T) {
	t.Skip("blocked: no fixture tool server exists to add for a named agent")
}
