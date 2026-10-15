package metrics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// figureRow is one run of a groups.json and runs.json fixture.
type figureRow struct {
	run, group bus.ID
	outcome    string
	endedAgo   time.Duration // how long ago the run ended; zero for a run that has not ended
}

// writeFigures writes a groups.json and a runs.json holding the given runs, each with one group.
func writeFigures(t *testing.T, dir string, rows ...figureRow) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	groups := map[bus.ID]map[bus.ID]GroupMetrics{}
	runs := map[bus.ID]RunMetrics{}
	for _, r := range rows {
		groups[r.run] = map[bus.ID]GroupMetrics{r.group: {Group: r.group, Run: r.run, Outcome: r.outcome}}
		rm := RunMetrics{Run: r.run, Dir: dir}
		if r.endedAgo > 0 {
			rm.EndedAt = time.Now().Add(-r.endedAgo)
		}
		runs[r.run] = rm
	}
	for name, v := range map[string]any{"groups.json": groups, "runs.json": runs} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// startAndStop starts the Service, opens the repository with a run-start, and stops it.
func startAndStop(f *fixture) {
	stop := f.start()
	f.publish(f.runStart("run-new", "g1"))
	stop()
}

// TestRetentionDropsOldRecords covers records 31 days old with retention 30: the start drops old session records and the figures of runs that ended before the cutoff, and keeps open and recent runs.
func TestRetentionDropsOldRecords(t *testing.T) {
	f := newFixture(t, 100, nil)
	old := 31 * 24 * time.Hour
	writeFigures(t, f.repoDir,
		figureRow{run: "run-old", group: "g1", outcome: "done", endedAgo: old},
		figureRow{run: "run-recent", group: "g1", outcome: "done", endedAgo: time.Hour},
		figureRow{run: "run-open", group: "g1", outcome: "running"})
	sessions := filepath.Join(f.repoDir, "sessions.jsonl")
	oldRec, _ := json.Marshal(SessionRecord{Session: "s-old", EndedAt: time.Now().Add(-old)})
	newRec, _ := json.Marshal(SessionRecord{Session: "s-new", EndedAt: time.Now().Add(-time.Hour)})
	if err := os.WriteFile(sessions, []byte(joinLines(string(oldRec), string(newRec), "{garbage")), 0o644); err != nil {
		t.Fatal(err)
	}
	startAndStop(f)
	if got := lines(t, sessions); len(got) != 2 || got[0] != string(newRec) || got[1] != "{garbage" {
		t.Fatalf("sessions.jsonl = %v, want the new record and the unparseable line", got)
	}
	runs, err := NewReader(f.repoDir).Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[bus.ID]bool{}
	for _, r := range runs {
		ids[r.Run] = true
	}
	if ids["run-old"] || !ids["run-recent"] || !ids["run-open"] || !ids["run-new"] {
		t.Fatalf("runs.json holds %v, want run-recent, run-open and run-new but not run-old", ids)
	}
	if gs, _ := NewReader(f.repoDir).Groups(context.Background(), "run-old"); len(gs) != 0 {
		t.Fatalf("groups of the dropped run remain: %+v", gs)
	}
}

// TestSessionRecordWithOnlyStartedAtIsAgedByIt checks a record from before ended_at existed falls back to started_at.
func TestSessionRecordWithOnlyStartedAtIsAgedByIt(t *testing.T) {
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	old, _ := json.Marshal(SessionRecord{Session: "a", StartedAt: cutoff.Add(-time.Hour)})
	zero, _ := json.Marshal(SessionRecord{Session: "b"})
	got := keepSince([][]byte{old, zero}, cutoff)
	if len(got) != 1 || string(got[0]) != string(zero) {
		t.Fatalf("kept %s, want only the record with no time", got)
	}
}

// TestTrimLeavesTheEventStoreAlone checks the Service's trim never touches the bus's event logs.
func TestTrimLeavesTheEventStoreAlone(t *testing.T) {
	f := newFixture(t, 100, nil)
	p := writeLog(t, f.events, "run-ancient", joinLines(jsonLine(t, doneEvent(0, "g1", "a"))))
	startAndStop(f)
	f.svc.trimKnown(context.Background())
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the Service deleted an event log: %v", err)
	}
}

// TestPeriodicTrimDropsRunsThatAged checks the trim the loop runs every 24 hours drops the figures of a run that ended before the cutoff.
func TestPeriodicTrimDropsRunsThatAged(t *testing.T) {
	f := newFixture(t, 100, nil)
	startAndStop(f)
	writeFigures(t, f.repoDir, figureRow{run: "run-late", group: "g1", outcome: "done", endedAgo: 40 * 24 * time.Hour})
	f.svc.trimKnown(context.Background())
	f.svc.flush()
	runs, _ := NewReader(f.repoDir).Runs(context.Background())
	for _, r := range runs {
		if r.Run == "run-late" {
			t.Fatalf("the aged run survived the periodic trim: %+v", r)
		}
	}
}
