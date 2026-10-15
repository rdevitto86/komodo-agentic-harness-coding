package metrics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// aged builds a task.done event whose time is d before now.
func aged(d time.Duration, run bus.ID) bus.Event {
	e := doneEvent(0, "g1", "a")
	e.Run = run
	e.Time = time.Now().UTC().Truncate(time.Second).Add(-d)
	return e
}

// figureRow is one run of a groups.json and runs.json fixture.
type figureRow struct {
	run, group bus.ID
	outcome    string
	ended      bool
}

// writeFigures writes a groups.json and a runs.json holding the given runs, each with one group.
func writeFigures(t *testing.T, dir string, rows ...figureRow) {
	t.Helper()
	groups := map[bus.ID]map[bus.ID]GroupMetrics{}
	runs := map[bus.ID]RunMetrics{}
	for _, r := range rows {
		groups[r.run] = map[bus.ID]GroupMetrics{r.group: {Group: r.group, Run: r.run, Outcome: r.outcome}}
		rm := RunMetrics{Run: r.run, Dir: dir}
		if r.ended {
			rm.EndedAt = time.Now()
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

// exists reports whether a file is present.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// startAndStop starts the Service, opens the repository with a run-start, and stops it.
func startAndStop(f *fixture) {
	stop := f.start()
	f.publish(f.runStart("run-new", "g1"))
	stop()
}

// TestRetentionDeletesOldLogs covers a log 31 days old with retention 30: the start deletes it unless its run has not ended.
func TestRetentionDeletesOldLogs(t *testing.T) {
	f := newFixture(t, 100, nil)
	old := 31 * 24 * time.Hour
	closed := writeLog(t, f.repoDir, "run-closed", joinLines(jsonLine(t, aged(old, "run-closed"))))
	open := writeLog(t, f.repoDir, "run-open", joinLines(jsonLine(t, aged(old, "run-open"))))
	fresh := writeLog(t, f.repoDir, "run-fresh", joinLines(jsonLine(t, aged(time.Hour, "run-fresh"))))
	writeFigures(t, f.repoDir,
		figureRow{run: "run-closed", group: "g1", outcome: "blocked", ended: true},
		figureRow{run: "run-open", group: "g1", outcome: "done"})
	sessions := filepath.Join(f.repoDir, "sessions.jsonl")
	oldRec, _ := json.Marshal(SessionRecord{Session: "s-old", EndedAt: time.Now().Add(-old)})
	newRec, _ := json.Marshal(SessionRecord{Session: "s-new", EndedAt: time.Now().Add(-time.Hour)})
	if err := os.WriteFile(sessions, []byte(joinLines(string(oldRec), string(newRec), "{garbage")), 0o644); err != nil {
		t.Fatal(err)
	}
	startAndStop(f)
	if exists(closed) || !exists(open) || !exists(fresh) {
		t.Fatalf("closed=%v open=%v fresh=%v, want false true true", exists(closed), exists(open), exists(fresh))
	}
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
	if ids["run-closed"] || !ids["run-open"] || !ids["run-new"] {
		t.Fatalf("runs.json holds %v, want run-open and run-new but not run-closed", ids)
	}
	gs, _ := NewReader(f.repoDir).Groups(context.Background(), "run-closed")
	if len(gs) != 0 {
		t.Fatalf("groups of the deleted run remain: %+v", gs)
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

// mkSparse makes a run log of mb megabytes with a modified time age ago.
func mkSparse(t *testing.T, dir string, run bus.ID, mb int64, age time.Duration) string {
	t.Helper()
	p := writeLog(t, dir, run, "")
	if err := os.Truncate(p, mb<<20); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSizeBoundTrimsOldestLogs covers files at 120 MB: after the trim they are at or below 80 MB and a log of a run that has not ended stays.
func TestSizeBoundTrimsOldestLogs(t *testing.T) {
	f := newFixture(t, 100, nil)
	oldest := mkSparse(t, f.repoDir, "run-1", 50, 10*24*time.Hour)
	newer := mkSparse(t, f.repoDir, "run-2", 50, 5*24*time.Hour)
	open := mkSparse(t, f.repoDir, "run-open", 20, 20*24*time.Hour)
	writeFigures(t, f.repoDir, figureRow{run: "run-open", group: "g1", outcome: "paused"})
	startAndStop(f)
	if exists(oldest) || !exists(newer) || !exists(open) {
		t.Fatalf("oldest=%v newer=%v open=%v, want false true true", exists(oldest), exists(newer), exists(open))
	}
	var total int64
	for _, p := range []string{newer, open} {
		st, _ := os.Stat(p)
		total += st.Size()
	}
	if total > 80<<20 {
		t.Fatalf("files total %d bytes, want at most 80 MB", total)
	}
}

// TestProtectedLogsOverTheBoundAreRecordedOnce checks protected logs alone past the bound give one record and are not deleted.
func TestProtectedLogsOverTheBoundAreRecordedOnce(t *testing.T) {
	old := sizeBound
	sizeBound = 10 << 20
	t.Cleanup(func() { sizeBound = old })
	f := newFixture(t, 100, nil)
	a := mkSparse(t, f.repoDir, "run-a", 8, 3*24*time.Hour)
	b := mkSparse(t, f.repoDir, "run-b", 8, 2*24*time.Hour)
	writeFigures(t, f.repoDir,
		figureRow{run: "run-a", group: "g1", outcome: "running"},
		figureRow{run: "run-b", group: "g1", outcome: "running"})
	startAndStop(f)
	f.svc.trimKnown(context.Background())
	if !exists(a) || !exists(b) {
		t.Fatal("a protected log was deleted")
	}
	got := strings.Join(lines(t, filepath.Join(f.homeDir, "engine.jsonl")), "\n")
	if n := strings.Count(got, `"kind":"size_bound"`); n != 1 {
		t.Fatalf("%d size_bound records, want 1:\n%s", n, got)
	}
}

// TestPeriodicTrimDeletesLogsThatAged checks the trim the loop runs every 24 hours deletes an aged log of an ended run.
func TestPeriodicTrimDeletesLogsThatAged(t *testing.T) {
	f := newFixture(t, 100, nil)
	startAndStop(f)
	p := writeLog(t, f.repoDir, "run-late", joinLines(jsonLine(t, aged(40*24*time.Hour, "run-late"))))
	f.svc.trimKnown(context.Background())
	if exists(p) {
		t.Fatal("the aged log survived the periodic trim")
	}
}
