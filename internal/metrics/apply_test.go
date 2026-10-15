package metrics

import (
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// start builds the run.start fact that creates a run and its groups.
func startRun(sec int, run bus.ID, groups ...bus.ID) bus.Event {
	return ev(sec, bus.TypeRunStatus, "run.start", run, "", &bus.RunStartedFact{Groups: groups})
}

// enter builds a stage.enter event.
func enter(sec int, run, group bus.ID, to string, ret bool) bus.Event {
	return ev(sec, bus.TypeRunStatus, "stage.enter", run, group, &bus.StageEnter{To: to, Return: ret})
}

// stageOf returns the stage row with the given name.
func stageOf(t *testing.T, g GroupMetrics, name string) StageTime {
	t.Helper()
	for _, s := range g.Stages {
		if s.Stage == name {
			return s
		}
	}
	t.Fatalf("group has no stage %q: %+v", name, g.Stages)
	return StageTime{}
}

// TestReturnToBuildCountsEntries covers build at t=0 and build again at t=100 with return: 2 entries and returns 1.
func TestReturnToBuildCountsEntries(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g"))
	f.apply(enter(0, "r", "g", "build", false))
	f.apply(enter(100, "r", "g", "build", true))
	g, _ := f.group("r", "g")
	b := stageOf(t, g, "build")
	if b.Entries != 2 || g.Returns != 1 || b.Seconds != 100 {
		t.Fatalf("build = %+v returns %d, want entries 2, seconds 100, returns 1", b, g.Returns)
	}
	f.apply(ev(130, bus.TypeRunStatus, "task.done", "r", "g", &bus.TaskDone{Task: "t"}))
	g, _ = f.group("r", "g")
	if got := stageOf(t, g, "build").Seconds; got != 130 {
		t.Fatalf("seconds after a later event = %d, want 130", got)
	}
}

// TestBlockedSeconds covers a group blocked at t=50 and continued at t=80: blocked_s 30.
func TestBlockedSeconds(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g"))
	f.apply(enter(0, "r", "g", "build", false))
	f.apply(ev(50, bus.TypeRunFailure, "group.blocked", "r", "g", &bus.GroupBlocked{GroupFacts: bus.GroupFacts{Reason: "tester"}}))
	if g, _ := f.group("r", "g"); g.Outcome != "blocked" {
		t.Fatalf("outcome = %q, want blocked", g.Outcome)
	}
	f.apply(enter(80, "r", "g", "build", false))
	g, _ := f.group("r", "g")
	if g.BlockedS != 30 || g.Outcome != "running" || stageOf(t, g, "build").Seconds != 50 {
		t.Fatalf("group = %+v, want blocked_s 30, running, build 50 s", g)
	}
}

// TestPausedSeconds checks a pause clock the way the blocked one runs.
func TestPausedSeconds(t *testing.T) {
	f := newFigures()
	f.apply(enter(0, "r", "g", "build", false))
	f.apply(ev(10, bus.TypeRunStatus, "group.paused", "r", "g", &bus.GroupPaused{}))
	f.apply(enter(25, "r", "g", "build", false))
	if g, _ := f.group("r", "g"); g.PausedS != 15 {
		t.Fatalf("paused_s = %d, want 15", g.PausedS)
	}
}

// TestUsageSums covers two sessions with usage: the group and run usage are their sum.
func TestUsageSums(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g"))
	u1 := &bus.SessionUsage{TokensIn: 100, TokensOut: 10, CacheRead: 50, USDEquiv: 0.5}
	u2 := &bus.SessionUsage{TokensIn: 200, TokensOut: 20, CacheWrite: 7, USD: 1.25, SkillTokens: 3}
	var recs []*SessionRecord
	for _, e := range append(session(0, "r", "g", "s1", "builder", u1), session(20, "r", "g", "s2", "builder", u2)...) {
		if r := f.apply(e); r != nil {
			recs = append(recs, r)
		}
	}
	g, _ := f.group("r", "g")
	want := bus.SessionUsage{TokensIn: 300, TokensOut: 30, CacheRead: 50, CacheWrite: 7, USD: 1.25, USDEquiv: 0.5, SkillTokens: 3}
	if g.Usage != want || g.Sessions != 2 {
		t.Fatalf("group usage = %+v sessions %d, want %+v and 2", g.Usage, g.Sessions, want)
	}
	if r, _ := f.run("r"); r.Usage != want {
		t.Fatalf("run usage = %+v, want %+v", r.Usage, want)
	}
	if len(recs) != 2 || recs[0].Agent != "builder" || recs[0].Task != "t-s1" || recs[0].Model != "m1" || recs[0].Stage != "build" ||
		recs[0].Reason != "done" || recs[0].WallS != 10 || recs[0].Usage == nil || recs[0].StartedAt != t0 {
		t.Fatalf("records = %+v", recs)
	}
}

// TestSessionWithoutUsage checks a session with no usage leaves the record's usage absent.
func TestSessionWithoutUsage(t *testing.T) {
	f := newFigures()
	var rec *SessionRecord
	for _, e := range session(0, "r", "g", "s1", "builder", nil) {
		if r := f.apply(e); r != nil {
			rec = r
		}
	}
	if rec == nil || rec.Usage != nil {
		t.Fatalf("record = %+v, want usage absent", rec)
	}
}

// TestSessionEndWithoutStart checks the record has empty agent and link and the session is counted.
func TestSessionEndWithoutStart(t *testing.T) {
	f := newFigures()
	rec := f.apply(session(0, "r", "g", "s9", "builder", nil)[1])
	if rec == nil || rec.Agent != "" || rec.Link != "" || rec.Session != "s9" || rec.Reason != "done" {
		t.Fatalf("record = %+v", rec)
	}
	if g, _ := f.group("r", "g"); g.Sessions != 1 {
		t.Fatalf("sessions = %d, want 1", g.Sessions)
	}
}

// TestCountersAndRunComplete checks task, retry, error counters and the closing of clocks at run-complete.
func TestCountersAndRunComplete(t *testing.T) {
	f := newFigures()
	f.apply(startRun(0, "r", "g1", "g2"))
	f.apply(enter(0, "r", "g1", "build", false))
	f.apply(ev(5, bus.TypeRunStatus, "task.retry", "r", "g1", &bus.TaskRetry{Task: "a", Attempt: 2}))
	f.apply(ev(6, bus.TypeRunStatus, "task.done", "r", "g1", &bus.TaskDone{Task: "a"}))
	f.apply(ev(7, bus.TypeError, "fault.error", "r", "g1", &errBody{Severity: "error"}))
	f.apply(ev(8, bus.TypeError, "fault.error", "r", "", &errBody{Severity: "warn"}))
	f.apply(ev(60, bus.TypeRunComplete, "done", "r", "", &bus.RunDone{Done: []bus.ID{"g1"}, Blocked: []bus.ID{"g2"}}))
	g1, _ := f.group("r", "g1")
	g2, _ := f.group("r", "g2")
	r, _ := f.run("r")
	if g1.Retries != 1 || g1.TasksDone != 1 || g1.Outcome != "done" || g1.EndedAt != t0.Add(60*time.Second) || stageOf(t, g1, "build").Seconds != 60 {
		t.Fatalf("g1 = %+v", g1)
	}
	if g2.Outcome != "blocked" || g2.EndedAt.IsZero() {
		t.Fatalf("g2 = %+v", g2)
	}
	if r.Faults != 1 || r.Warnings != 1 || r.WallS != 60 || r.Done != 1 || r.Blocked != 1 || r.Groups != 2 || r.EndedAt.IsZero() {
		t.Fatalf("run = %+v", r)
	}
}
