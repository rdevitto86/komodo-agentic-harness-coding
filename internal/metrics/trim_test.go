package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// writeFigures writes a groups.json holding one group in the given outcome.
func writeFigures(t *testing.T, dir string, run, group bus.ID, outcome string) {
	t.Helper()
	b, err := json.Marshal(map[bus.ID]GroupMetrics{group: {Group: group, Run: run, Outcome: outcome}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "groups.json"), b, 0o644); err != nil {
		t.Fatal(err)
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

// TestRetentionDeletesOldLogs covers a log 31 days old with retention 30: the start deletes it, a log of an open group stays.
func TestRetentionDeletesOldLogs(t *testing.T) {
	f := newFixture(t, 100, nil)
	old := 31 * 24 * time.Hour
	closed := writeLog(t, f.repoDir, "run-closed", joinLines(jsonLine(t, aged(old, "run-closed"))))
	open := writeLog(t, f.repoDir, "run-open", joinLines(jsonLine(t, aged(old, "run-open"))))
	fresh := writeLog(t, f.repoDir, "run-fresh", joinLines(jsonLine(t, aged(time.Hour, "run-fresh"))))
	writeFigures(t, f.repoDir, "run-open", "g1", "blocked")
	oldRec, _ := json.Marshal(SessionRecord{Session: "s-old", StartedAt: time.Now().Add(-old)})
	newRec, _ := json.Marshal(SessionRecord{Session: "s-new", StartedAt: time.Now().Add(-time.Hour)})
	sessions := filepath.Join(f.repoDir, "sessions.jsonl")
	if err := os.WriteFile(sessions, []byte(joinLines(string(oldRec), string(newRec))), 0o644); err != nil {
		t.Fatal(err)
	}
	startAndStop(f)
	if exists(closed) || !exists(open) || !exists(fresh) {
		t.Fatalf("closed=%v open=%v fresh=%v, want false true true", exists(closed), exists(open), exists(fresh))
	}
	got := lines(t, sessions)
	if len(got) != 1 || got[0] != string(newRec) {
		t.Fatalf("sessions.jsonl = %v, want only the new record", got)
	}
}

// TestSizeBoundTrimsOldestLogs covers files at 120 MB: after the trim they are at or below 80 MB and an open group's log stays.
func TestSizeBoundTrimsOldestLogs(t *testing.T) {
	f := newFixture(t, 100, nil)
	mk := func(run bus.ID, mb int64, age time.Duration) string {
		p := writeLog(t, f.repoDir, run, "")
		if err := os.Truncate(p, mb<<20); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldest := mk("run-1", 50, 10*24*time.Hour)
	newer := mk("run-2", 50, 5*24*time.Hour)
	open := mk("run-open", 20, 20*24*time.Hour)
	writeFigures(t, f.repoDir, "run-open", "g1", "paused")
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
