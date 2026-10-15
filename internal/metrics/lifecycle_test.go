package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// groupDone stands in for the data of run-status / group.done, which another package owns.
type groupDone struct {
	Outcome string `json:"outcome"`
}

// EventKey binds the stand-in to run-status / group.done.
func (groupDone) EventKey() bus.Key { return bus.Key{Type: bus.TypeRunStatus, Subtype: "group.done"} }

// resumed builds a group.resumed event.
func resumed(sec int, run, group bus.ID, stage string) bus.Event {
	return ev(sec, bus.TypeRunStatus, "group.resumed", run, group, &bus.GroupResumed{From: stage, To: stage})
}

// blocked builds a group.blocked event.
func blocked(sec int, run, group bus.ID) bus.Event {
	return ev(sec, bus.TypeRunFailure, "group.blocked", run, group, &bus.GroupBlocked{})
}

// TestResumedStopsTheHoldClock checks group.resumed ends the blocked clock before any stage.enter.
func TestResumedStopsTheHoldClock(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g"))
	f.apply(enter(0, "r", "g", "build", false))
	f.apply(blocked(50, "r", "g"))
	f.apply(resumed(80, "r", "g", "build"))
	f.apply(ev(100, bus.TypeRunStatus, "task.done", "r", "g", &bus.TaskDone{Task: "a"}))
	g, _ := f.group("r", "g")
	if g.BlockedS != 30 || g.Outcome != "running" || stageOf(t, g, "build").Seconds != 50 {
		t.Fatalf("group = %+v, want blocked_s 30, running, build 50 s", g)
	}
}

// TestRunCompleteStoppedClosesTheFigures checks run-complete / stopped ends the run and its open groups as stopped.
func TestRunCompleteStoppedClosesTheFigures(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2"))
	f.apply(enter(0, "r", "g1", "build", false))
	f.apply(ev(60, bus.TypeRunComplete, "stopped", "r", "", &bus.RunStopped{Saved: []bus.ID{"g1"}}))
	for _, id := range []bus.ID{"g1", "g2"} {
		if g, _ := f.group("r", id); g.Outcome != "stopped" || g.EndedAt.IsZero() {
			t.Fatalf("%s = %+v, want stopped and ended", id, g)
		}
	}
	if r, _ := f.run("r"); r.EndedAt.IsZero() || r.WallS != 60 {
		t.Fatalf("run = %+v, want ended with wall 60", r)
	}
}

// TestLeaseLostFailsTheGroup checks run-failure / lease.lost ends the group as failed.
func TestLeaseLostFailsTheGroup(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2"))
	f.apply(enter(0, "r", "g1", "build", false))
	f.apply(ev(30, bus.TypeRunFailure, "lease.lost", "r", "g1", &bus.LeaseLost{Where: "renew"}))
	g, _ := f.group("r", "g1")
	if g.Outcome != "failed" || g.EndedAt != t0.Add(30*time.Second) || stageOf(t, g, "build").Seconds != 30 {
		t.Fatalf("g1 = %+v", g)
	}
	if g2, _ := f.group("r", "g2"); !g2.EndedAt.IsZero() {
		t.Fatalf("g2 ended: %+v", g2)
	}
}

// TestStalledEndsTheRun checks run-failure / stalled ends the run, keeps blocked groups blocked and fails the rest.
func TestStalledEndsTheRun(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2"))
	f.apply(enter(0, "r", "g1", "build", false))
	f.apply(enter(0, "r", "g2", "build", false))
	f.apply(blocked(40, "r", "g1"))
	f.apply(ev(70, bus.TypeRunFailure, "stalled", "r", "", &stalled{}))
	g1, _ := f.group("r", "g1")
	g2, _ := f.group("r", "g2")
	r, _ := f.run("r")
	if g1.Outcome != "blocked" || g1.BlockedS != 30 || g2.Outcome != "failed" || r.EndedAt.IsZero() || r.WallS != 70 {
		t.Fatalf("g1 %+v g2 %+v run %+v", g1, g2, r)
	}
}

// stalled stands in for the data of run-failure / stalled, which another package owns.
type stalled struct{}

// EventKey binds the stand-in to run-failure / stalled.
func (stalled) EventKey() bus.Key { return bus.Key{Type: bus.TypeRunFailure, Subtype: "stalled"} }

// TestGroupDoneOutcomes checks group.done maps complete, abandoned and lost to done, abandoned and failed.
func TestGroupDoneOutcomes(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2", "g3"))
	f.apply(ev(10, bus.TypeRunStatus, "group.done", "r", "g1", &groupDone{Outcome: "complete"}))
	f.apply(ev(11, bus.TypeRunStatus, "group.done", "r", "g2", &groupDone{Outcome: "abandoned"}))
	f.apply(ev(12, bus.TypeRunStatus, "group.done", "r", "g3", &groupDone{Outcome: "lost"}))
	for id, want := range map[bus.ID]string{"g1": "done", "g2": "abandoned", "g3": "failed"} {
		if g, _ := f.group("r", id); g.Outcome != want || g.EndedAt.IsZero() {
			t.Errorf("%s = %+v, want %s and ended", id, g, want)
		}
	}
}

// TestEarlierAttemptIsInterrupted checks a run.start for a group whose earlier attempt never ended closes that attempt.
func TestEarlierAttemptIsInterrupted(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r1", "g1"))
	f.apply(enter(0, "r1", "g1", "build", false))
	f.apply(ev(30, bus.TypeRunStatus, "task.done", "r1", "g1", &bus.TaskDone{Task: "a"}))
	f.apply(startRun(100, "r2", "g1"))
	old, _ := f.group("r1", "g1")
	if old.Outcome != "interrupted" || old.EndedAt != t0.Add(30*time.Second) {
		t.Fatalf("earlier attempt = %+v, want interrupted at 30 s", old)
	}
	if r, _ := f.run("r1"); r.EndedAt.IsZero() || r.WallS != 30 {
		t.Fatalf("earlier run = %+v, want ended with wall 30", r)
	}
	if g, ok := f.group("r2", "g1"); !ok || g.Outcome != "running" {
		t.Fatalf("new attempt = %+v, %v", g, ok)
	}
}

// TestGroupsAreKeyedByRunAndGroup checks one group id in two runs keeps two sets of figures.
func TestGroupsAreKeyedByRunAndGroup(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r1", "g1"))
	f.apply(ev(5, bus.TypeRunStatus, "task.done", "r1", "g1", &bus.TaskDone{Task: "a"}))
	f.apply(ev(6, bus.TypeRunComplete, "done", "r1", "", &bus.RunDone{Done: []bus.ID{"g1"}}))
	f.apply(startRun(10, "r2", "g1"))
	a, _ := f.group("r1", "g1")
	b, _ := f.group("r2", "g1")
	if a.TasksDone != 1 || b.TasksDone != 0 || a.Run != "r1" || b.Run != "r2" {
		t.Fatalf("r1/g1 %+v, r2/g1 %+v", a, b)
	}
	groups, _ := f.snapshot()
	if len(groups) != 2 {
		t.Fatalf("snapshot holds %d runs, want 2", len(groups))
	}
}

// TestSessionRecordCarriesEndTime checks a record holds ended_at, and a missing start leaves started_at zero.
func TestSessionRecordCarriesEndTime(t *testing.T) {
	f := newFigures()
	rec := f.apply(session(0, "r", "g", "s9", "builder", nil)[1])
	if rec.EndedAt != t0.Add(10*time.Second) || !rec.StartedAt.IsZero() {
		t.Fatalf("record = %+v", rec)
	}
}

// openRun handles a run-start so the Service treats the run as open.
func openRun(t *testing.T, f *fixture, run bus.ID) {
	t.Helper()
	rs := f.runStart(run, "g1")
	rs.Time = t0
	f.svc.handle(context.Background(), rs)
}

// flood publishes enough events to overflow the Service's queue.
func flood(f *fixture) {
	for range 10 {
		f.publish(bus.Event{Type: bus.TypeRunStop, Data: &bus.RunStop{Target: "x"}})
	}
}

// TestGapGoesToEveryOpenRun checks a drop leaves a gap line in the log of each run open at that moment.
func TestGapGoesToEveryOpenRun(t *testing.T) {
	f := newFixture(t, 3, nil)
	openRun(t, f, "run-a")
	openRun(t, f, "run-b")
	flood(f)
	f.svc.handle(context.Background(), <-f.svc.sub.Events)
	for _, run := range []bus.ID{"run-a", "run-b"} {
		got := strings.Join(lines(t, filepath.Join(f.repoDir, LogName(run))), "\n")
		if !strings.Contains(got, `"gap":true`) || !strings.Contains(got, `"dropped":`) {
			t.Errorf("log of %s has no gap line:\n%s", run, got)
		}
	}
	if got := strings.Join(lines(t, filepath.Join(f.homeDir, "engine.jsonl")), ""); strings.Contains(got, `"gap":true`) {
		t.Errorf("engine.jsonl holds a gap line although runs were open")
	}
}

// TestGapWithNoOpenRunGoesToMachineLog checks a drop with no open run is marked in engine.jsonl.
func TestGapWithNoOpenRunGoesToMachineLog(t *testing.T) {
	f := newFixture(t, 3, nil)
	flood(f)
	f.svc.handle(context.Background(), <-f.svc.sub.Events)
	got := strings.Join(lines(t, filepath.Join(f.homeDir, "engine.jsonl")), "\n")
	if !strings.Contains(got, `"gap":true`) {
		t.Fatalf("engine.jsonl has no gap line:\n%s", got)
	}
	if f.svc.lastDropped == 0 {
		t.Fatal("the drop count did not advance after a successful write")
	}
}

// TestGapIsRetriedWhenTheWriteFails checks the drop count advances only after the gap line was written.
func TestGapIsRetriedWhenTheWriteFails(t *testing.T) {
	f := newFixture(t, 3, nil)
	if err := os.MkdirAll(filepath.Join(f.repoDir, LogName("run-a")), 0o755); err != nil {
		t.Fatal(err)
	}
	openRun(t, f, "run-a")
	flood(f)
	f.svc.handle(context.Background(), <-f.svc.sub.Events)
	if f.svc.lastDropped != 0 {
		t.Fatalf("lastDropped = %d after a failed gap write, want 0", f.svc.lastDropped)
	}
}

// TestRunStartWithoutCommonDirIsRecorded checks a run-start that names no git directory is recorded once in engine.jsonl and writes no run log.
func TestRunStartWithoutCommonDirIsRecorded(t *testing.T) {
	f := newFixture(t, 100, nil)
	rs := f.runStart("run-a", "g1")
	rs.Data.(*bus.RunStart).CommonDir = ""
	stop := f.start()
	f.publish(rs)
	stop()
	got := strings.Join(lines(t, filepath.Join(f.homeDir, "engine.jsonl")), "\n")
	if strings.Count(got, `"kind":"no_common_dir"`) != 1 || exists(filepath.Join(f.repoDir, LogName("run-a"))) {
		t.Fatalf("engine.jsonl = %s, want one no_common_dir record and no run log", got)
	}
}

// TestRunEndPrunesTheMaps checks run-complete drops the run from the Service's in-memory maps.
func TestRunEndPrunesTheMaps(t *testing.T) {
	f := newFixture(t, 100, nil)
	openRun(t, f, "run-a")
	if _, ok := f.svc.runRepo["run-a"]; !ok {
		t.Fatal("the open run is not tracked")
	}
	f.svc.handle(context.Background(), ev(5, bus.TypeRunComplete, "done", "run-a", "", &bus.RunDone{}))
	if _, ok := f.svc.runRepo["run-a"]; ok {
		t.Fatal("the ended run is still tracked")
	}
	if len(f.svc.logs) != 0 {
		t.Fatalf("%d logs still open", len(f.svc.logs))
	}
}

// TestUnknownRunFindsItsFolderInRunsJSON checks a restarted Service places an event of an earlier run by that folder's runs.json.
func TestUnknownRunFindsItsFolderInRunsJSON(t *testing.T) {
	f := newFixture(t, 100, nil)
	openRun(t, f, "run-a")
	f.svc.flush()
	again, err := New(f.bus, f.homeDir, Options{RetentionDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	again.handle(context.Background(), ev(9, bus.TypeRunStop, "", "run-a", "", &bus.RunStop{Target: "late"}))
	got := lines(t, filepath.Join(f.repoDir, LogName("run-a")))
	if len(got) != 2 || !strings.Contains(got[1], "late") {
		t.Fatalf("log = %v, want the late event appended", got)
	}
}

// TestRebuildCoversKnownRepositoriesOnAFreshEngine checks Rebuild on a new Service rewrites figures for repositories the machine folder lists.
func TestRebuildCoversKnownRepositoriesOnAFreshEngine(t *testing.T) {
	f := newFixture(t, 100, nil)
	stop := f.start()
	f.publish(f.runStart("run-a", "g1"),
		bus.Event{Type: bus.TypeRunStatus, Subtype: "run.start", Run: "run-a", Data: &bus.RunStartedFact{Groups: []bus.ID{"g1"}}})
	stop()
	for _, n := range []string{"groups.json", "runs.json"} {
		if err := os.Remove(filepath.Join(f.repoDir, n)); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := New(f.bus, f.homeDir, Options{RetentionDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, err := NewReader(f.repoDir).Runs(context.Background())
	if err != nil || len(runs) != 1 || runs[0].Dir != f.repoDir {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
}
