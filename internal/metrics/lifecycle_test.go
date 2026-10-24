package metrics

import (
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// stalled stands in for the data of run-failure / stalled, which another package owns.
type stalled struct{}

// EventKey binds the stand-in to run-failure / stalled.
func (stalled) EventKey() bus.Key { return bus.Key{Type: bus.TypeRunFailure, Subtype: "stalled"} }

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

// TestGroupDoneOutcomes checks group.done maps complete, abandoned and lost to done, abandoned and failed.
func TestGroupDoneOutcomes(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2", "g3"))
	f.apply(ev(10, bus.TypeRunStatus, "group.done", "r", "g1", &bus.GroupDone{Outcome: "complete"}))
	f.apply(ev(11, bus.TypeRunStatus, "group.done", "r", "g2", &bus.GroupDone{Outcome: "abandoned"}))
	f.apply(ev(12, bus.TypeRunStatus, "group.done", "r", "g3", &bus.GroupDone{Outcome: "lost"}))
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

// resumed builds a group.resumed event.
func resumed(sec int, run, group bus.ID, stage string) bus.Event {
	return ev(sec, bus.TypeRunStatus, "group.resumed", run, group, &bus.GroupResumed{From: stage, To: stage})
}

// blocked builds a group.blocked event.
func blocked(sec int, run, group bus.ID) bus.Event {
	return ev(sec, bus.TypeRunFailure, "group.blocked", run, group, &bus.GroupBlocked{})
}
