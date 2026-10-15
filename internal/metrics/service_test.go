package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// session builds a session.start and session.end pair for one session.
func session(sec int, run, group, id bus.ID, agent string, u *bus.SessionUsage) []bus.Event {
	return []bus.Event{
		ev(sec, bus.TypeRunStatus, "session.start", run, group, &bus.SessionStart{GroupFacts: facts("build", id), Agent: agent, Task: "t-" + string(id), Link: "claude-cli", Model: "m1"}),
		ev(sec+10, bus.TypeRunStatus, "session.end", run, group, &bus.SessionEnd{GroupFacts: bus.GroupFacts{Stage: "build", Session: id, Reason: "done"}, Agent: agent, Link: "claude-cli", WallS: 10, Usage: u}),
	}
}

// TestRunLogHoldsEveryEvent covers a run with 2 groups and 3 sessions: every event is a log line and sessions.jsonl holds 3 records.
func TestRunLogHoldsEveryEvent(t *testing.T) {
	f := newFixture(t, 100, nil)
	run := bus.ID("run-a")
	evs := []bus.Event{
		f.runStart(run, "g1", "g2"),
		{Type: bus.TypeRunStatus, Subtype: "run.start", Run: run, Data: &bus.RunStartedFact{Groups: []bus.ID{"g1", "g2"}}},
	}
	evs = append(evs, session(0, run, "g1", "s1", "builder", nil)...)
	evs = append(evs, session(0, run, "g1", "s2", "tester", nil)...)
	evs = append(evs, session(0, run, "g2", "s3", "builder", nil)...)
	evs = append(evs, bus.Event{Type: bus.TypeRunComplete, Subtype: "done", Run: run, Data: &bus.RunDone{Done: []bus.ID{"g1", "g2"}}})
	stop := f.start()
	f.publish(evs...)
	stop()

	got := lines(t, filepath.Join(f.repoDir, LogName(run)))
	if len(got) != len(evs) {
		t.Fatalf("log has %d lines, want %d", len(got), len(evs))
	}
	for i, l := range got {
		if !strings.HasPrefix(l, `{"type":"`) {
			t.Errorf("line %d is not an event line: %s", i, l)
		}
	}
	if n := len(lines(t, filepath.Join(f.repoDir, "sessions.jsonl"))); n != 3 {
		t.Fatalf("sessions.jsonl has %d records, want 3", n)
	}
	rm, err := NewReader(f.repoDir).Runs(context.Background())
	if err != nil || len(rm) != 1 || rm[0].Groups != 2 || rm[0].Done != 2 {
		t.Fatalf("Runs = %+v, %v", rm, err)
	}
}

// TestTwoRunsOneBus checks each event lands in the log of its own run, in arrival order.
func TestTwoRunsOneBus(t *testing.T) {
	f := newFixture(t, 100, nil)
	stop := f.start()
	f.publish(f.runStart("run-a", "g1"), f.runStart("run-b", "g1"),
		bus.Event{Type: bus.TypeRunStop, Run: "run-b", Data: &bus.RunStop{Target: "b-1"}},
		bus.Event{Type: bus.TypeRunStop, Run: "run-a", Data: &bus.RunStop{Target: "a-1"}},
		bus.Event{Type: bus.TypeRunStop, Run: "run-b", Data: &bus.RunStop{Target: "b-2"}},
	)
	stop()
	a := lines(t, filepath.Join(f.repoDir, LogName("run-a")))
	b := lines(t, filepath.Join(f.repoDir, LogName("run-b")))
	if len(a) != 2 || len(b) != 3 {
		t.Fatalf("lines a=%d b=%d, want 2 and 3", len(a), len(b))
	}
	if !strings.Contains(b[1], "b-1") || !strings.Contains(b[2], "b-2") || strings.Contains(strings.Join(a, ""), "b-") {
		t.Fatalf("order or routing wrong:\na=%v\nb=%v", a, b)
	}
}

// TestEventWithoutRunGoesToMachineLog checks an event naming no run goes to engine.jsonl.
func TestEventWithoutRunGoesToMachineLog(t *testing.T) {
	f := newFixture(t, 100, nil)
	stop := f.start()
	f.publish(bus.Event{Type: bus.TypeRunStop, Data: &bus.RunStop{Target: "ep-1"}})
	stop()
	got := lines(t, filepath.Join(f.homeDir, "engine.jsonl"))
	if len(got) != 1 || !strings.Contains(got[0], `"target":"ep-1"`) {
		t.Fatalf("engine.jsonl = %v", got)
	}
}

// TestKilledBeforeAppend checks an event published to a Service that never ran is absent from the log.
func TestKilledBeforeAppend(t *testing.T) {
	f := newFixture(t, 100, nil)
	f.publish(f.runStart("run-a", "g1"))
	if _, err := os.Stat(filepath.Join(f.repoDir, LogName("run-a"))); !os.IsNotExist(err) {
		t.Fatalf("log exists or errored unexpectedly: %v", err)
	}
}

// TestQueueOverflowWritesGapLine checks a queue that overflowed leaves a gap line in the run's log.
func TestQueueOverflowWritesGapLine(t *testing.T) {
	f := newFixture(t, 3, nil)
	f.publish(f.runStart("run-a", "g1"))
	for range 10 {
		f.publish(bus.Event{Type: bus.TypeRunStop, Run: "run-a", Data: &bus.RunStop{Target: "x"}})
	}
	if f.svc.sub.Dropped() == 0 {
		t.Fatal("fixture dropped nothing")
	}
	stop := f.start()
	stop()
	var gaps int
	for _, l := range lines(t, filepath.Join(f.repoDir, LogName("run-a"))) {
		if strings.Contains(l, `"gap":true`) {
			gaps++
			if !strings.Contains(l, `"dropped":`) {
				t.Errorf("gap line has no count: %s", l)
			}
		}
	}
	if gaps != 1 {
		t.Fatalf("%d gap lines, want 1", gaps)
	}
}

// TestSecretIsMasked checks a line holding a secret is written masked.
func TestSecretIsMasked(t *testing.T) {
	mask := func(s string) string { return strings.ReplaceAll(s, "sk-SECRET", "***") }
	f := newFixture(t, 100, mask)
	stop := f.start()
	f.publish(f.runStart("run-a", "g1"),
		bus.Event{Type: bus.TypeRunFailure, Subtype: "group.blocked", Run: "run-a", Group: "g1", Data: &bus.GroupBlocked{Detail: "key sk-SECRET leaked"}})
	stop()
	raw, err := os.ReadFile(filepath.Join(f.repoDir, LogName("run-a")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-SECRET") || !strings.Contains(string(raw), "key *** leaked") {
		t.Fatalf("log not masked: %s", raw)
	}
}

// TestFailedWriteReportsOnce checks a failing log gives one fault report per failure streak and the loop keeps reading.
func TestFailedWriteReportsOnce(t *testing.T) {
	f := newFixture(t, 100, nil)
	if err := os.MkdirAll(filepath.Join(f.repoDir, LogName("run-bad")), 0o755); err != nil {
		t.Fatal(err)
	}
	stop := f.start()
	f.publish(f.runStart("run-bad", "g1"), f.runStart("run-ok", "g1"))
	for range 3 {
		f.publish(bus.Event{Type: bus.TypeRunStop, Run: "run-bad", Data: &bus.RunStop{Target: "x"}})
	}
	f.publish(bus.Event{Type: bus.TypeRunStop, Run: "run-ok", Data: &bus.RunStop{Target: "y"}})
	stop()
	var reports int
	for len(f.errs.Events) > 0 {
		if e := <-f.errs.Events; e.Run == "run-bad" {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("%d reports, want 1", reports)
	}
	if n := len(lines(t, filepath.Join(f.repoDir, LogName("run-ok")))); n != 2 {
		t.Fatalf("run-ok log has %d lines, want 2", n)
	}
}

// TestRebuildRewritesFigures checks Rebuild recomputes groups.json and runs.json from the run logs.
func TestRebuildRewritesFigures(t *testing.T) {
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
	if err := f.svc.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	gs, err := NewReader(f.repoDir).Groups(context.Background(), "run-a")
	if err != nil || len(gs) != 1 || gs[0].Group != "g1" {
		t.Fatalf("Groups = %+v, %v", gs, err)
	}
}
