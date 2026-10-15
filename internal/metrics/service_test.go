package metrics

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

// dirNames lists the file names of a folder, or none when it is missing.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// TestFiguresFollowTheEvents covers a run with 2 groups and 3 sessions: sessions.jsonl holds 3 records, the figures count the groups, and the bus, not the Service, holds the run's event log.
func TestFiguresFollowTheEvents(t *testing.T) {
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

	if n := len(lines(t, filepath.Join(f.repoDir, "sessions.jsonl"))); n != 3 {
		t.Fatalf("sessions.jsonl has %d records, want 3", n)
	}
	rm, err := NewReader(f.repoDir).Runs(context.Background())
	if err != nil || len(rm) != 1 || rm[0].Groups != 2 || rm[0].Done != 2 {
		t.Fatalf("Runs = %+v, %v", rm, err)
	}
	if got := lines(t, filepath.Join(f.events, bus.LogName(run))); len(got) != len(evs) {
		t.Fatalf("the bus store holds %d lines, want %d", len(got), len(evs))
	}
}

// TestServiceWritesNoEventLog checks the Service's folders hold only its own records, for events with a run and without one.
func TestServiceWritesNoEventLog(t *testing.T) {
	f := newFixture(t, 100, nil)
	stop := f.start()
	f.publish(f.runStart("run-a", "g1"), bus.Event{Type: bus.TypeRunStop, Run: "run-a", Data: &bus.RunStop{Target: "x"}},
		bus.Event{Type: bus.TypeRunStop, Data: &bus.RunStop{Target: "ep-1"}})
	stop()
	for _, n := range dirNames(t, f.repoDir) {
		if !slices.Contains([]string{"groups.json", "runs.json", "sessions.jsonl"}, n) {
			t.Errorf("metrics folder holds %s", n)
		}
	}
	for _, n := range dirNames(t, f.homeDir) {
		if strings.HasSuffix(n, ".jsonl") && n != failuresFile {
			t.Errorf("machine metrics folder holds the event log %s", n)
		}
	}
}

// TestFailedRecordWriteIsRecordedOnce checks a failing sessions.jsonl gives one failure record in the run's figures and the loop keeps reading.
func TestFailedRecordWriteIsRecordedOnce(t *testing.T) {
	f := newFixture(t, 100, nil)
	ctx := context.Background()
	openRun(t, f, "run-bad")
	f.svc.handle(ctx, startRun(1, "run-bad", "g1"))
	if err := os.MkdirAll(filepath.Join(f.repoDir, "sessions.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range append(session(0, "run-bad", "g1", "s1", "builder", nil), session(20, "run-bad", "g1", "s2", "builder", nil)...) {
		f.svc.handle(ctx, e)
	}
	f.svc.flush()
	runs, err := NewReader(f.repoDir).Runs(context.Background())
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	if fl := runs[0].Failures; len(fl) != 1 || fl[0].Kind != "write" || fl[0].Time.IsZero() {
		t.Fatalf("failures = %+v, want one write failure", fl)
	}
	if got := lines(t, filepath.Join(f.homeDir, failuresFile)); len(got) != 0 {
		t.Fatalf("%s = %v, want nothing for a failure that has a run", failuresFile, got)
	}
}

// TestRebuildRewritesFigures checks Rebuild recomputes groups.json and runs.json from the bus store.
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
