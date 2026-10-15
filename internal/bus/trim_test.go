package bus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// aged builds a task.done event of a run whose time is d before now.
func aged(d time.Duration, run ID) Event {
	e := doneEvent(0, "g1", "a")
	e.Run = run
	e.Time = time.Now().UTC().Truncate(time.Second).Add(-d)
	return e
}

// exists reports whether a file is present.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// openRuns returns an open check that names the given runs.
func openRuns(ids ...ID) func(ID) bool {
	return func(r ID) bool {
		for _, id := range ids {
			if id == r {
				return true
			}
		}
		return false
	}
}

// mkSparse makes a run log of mb megabytes with a modified time age ago.
func mkSparse(t *testing.T, dir string, run ID, mb int64, age time.Duration) string {
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

// A log 31 days old with retention 30 is deleted unless its run is open; a fresh log stays.
func TestTrimDeletesLogsPastRetention(t *testing.T) {
	dir := t.TempDir()
	old := 31 * 24 * time.Hour
	closed := writeLog(t, dir, "run-closed", joinLines(jsonLine(t, aged(old, "run-closed"))))
	open := writeLog(t, dir, "run-open", joinLines(jsonLine(t, aged(old, "run-open"))))
	fresh := writeLog(t, dir, "run-fresh", joinLines(jsonLine(t, aged(time.Hour, "run-fresh"))))
	res, err := Trim(context.Background(), dir, TrimOptions{RetentionDays: 30, Open: openRuns("run-open")})
	if err != nil {
		t.Fatal(err)
	}
	if exists(closed) || !exists(open) || !exists(fresh) {
		t.Fatalf("closed=%v open=%v fresh=%v, want false true true", exists(closed), exists(open), exists(fresh))
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "run-closed" {
		t.Fatalf("deleted %v", res.Deleted)
	}
}

// With no retention given the default of 30 days applies.
func TestTrimDefaultsToThirtyDays(t *testing.T) {
	dir := t.TempDir()
	p := writeLog(t, dir, "run-old", joinLines(jsonLine(t, aged(31*24*time.Hour, "run-old"))))
	q := writeLog(t, dir, "run-new", joinLines(jsonLine(t, aged(29*24*time.Hour, "run-new"))))
	if _, err := Trim(context.Background(), dir, TrimOptions{}); err != nil {
		t.Fatal(err)
	}
	if exists(p) || !exists(q) {
		t.Fatalf("old=%v new=%v, want false true", exists(p), exists(q))
	}
}

// Files at 120 MB come down to at most 80 MB, oldest first, and a log of an open run stays.
func TestSizeBoundTrimsOldestLogs(t *testing.T) {
	dir := t.TempDir()
	oldest := mkSparse(t, dir, "run-1", 50, 10*24*time.Hour)
	newer := mkSparse(t, dir, "run-2", 50, 5*24*time.Hour)
	open := mkSparse(t, dir, "run-open", 20, 20*24*time.Hour)
	res, err := Trim(context.Background(), dir, TrimOptions{RetentionDays: 60, Open: openRuns("run-open")})
	if err != nil {
		t.Fatal(err)
	}
	if exists(oldest) || !exists(newer) || !exists(open) {
		t.Fatalf("oldest=%v newer=%v open=%v, want false true true", exists(oldest), exists(newer), exists(open))
	}
	if res.OverBound {
		t.Fatal("OverBound set although the trim reached the target")
	}
}

// Logs of open runs that alone pass the bound are kept and reported.
func TestOpenLogsOverTheBoundAreReported(t *testing.T) {
	old := sizeBound
	sizeBound = 10 << 20
	t.Cleanup(func() { sizeBound = old })
	dir := t.TempDir()
	a := mkSparse(t, dir, "run-a", 8, 3*24*time.Hour)
	b := mkSparse(t, dir, "run-b", 8, 2*24*time.Hour)
	res, err := Trim(context.Background(), dir, TrimOptions{Open: openRuns("run-a", "run-b")})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(a) || !exists(b) || !res.OverBound || res.ProtectedBytes != 16<<20 {
		t.Fatalf("a=%v b=%v res=%+v", exists(a), exists(b), res)
	}
}

// A log with no readable line is aged by its file time.
func TestTrimAgesAnUnparseableLogByFileTime(t *testing.T) {
	dir := t.TempDir()
	p := writeLog(t, dir, "run-x", "{garbage\n")
	mt := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	if _, err := Trim(context.Background(), dir, TrimOptions{RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if exists(p) {
		t.Fatal("the aged log survived")
	}
}

// A missing folder trims to nothing.
func TestTrimOfAMissingFolder(t *testing.T) {
	if res, err := Trim(context.Background(), filepath.Join(t.TempDir(), "none"), TrimOptions{}); err != nil || len(res.Deleted) != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

// The first run-start in a folder trims it in the background and drops the deleted runs from runs.json.
func TestRunStartTrimsItsFolder(t *testing.T) {
	f := newStoreBus(t, 100, func(o *Options) { o.RetentionDays = 30 })
	old := writeLog(t, f.repo, "run-old", joinLines(jsonLine(t, aged(40*24*time.Hour, "run-old"))))
	m, _ := json.Marshal(map[ID]string{"run-old": f.repo})
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, "runs.json"), m, 0o644); err != nil {
		t.Fatal(err)
	}
	b := New(Options{BufferEvents: 10, EventsDir: f.home, RetentionDays: 30})
	if err := b.Publish(f.runStart("run-new")); err != nil {
		t.Fatal(err)
	}
	// Close waits for the background trim.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if exists(old) {
		t.Fatal("the aged log survived the run-start trim")
	}
	raw, _ := os.ReadFile(filepath.Join(f.home, "runs.json"))
	var got map[ID]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["run-old"]; ok || got["run-new"] != f.repo {
		t.Fatalf("runs.json = %v, want run-new only", got)
	}
}

// The background trim asks the open check the engine passed in, and spares the logs of the runs it names.
func TestRunStartTrimHonoursTheOpenCheck(t *testing.T) {
	f := newStoreBus(t, 100, func(o *Options) { o.RetentionDays = 1; o.RunOpen = openRuns("run-paused") })
	paused := writeLog(t, f.repo, "run-paused", joinLines(jsonLine(t, aged(5*24*time.Hour, "run-paused"))))
	gone := writeLog(t, f.repo, "run-gone", joinLines(jsonLine(t, aged(5*24*time.Hour, "run-gone"))))
	f.publish(f.runStart("run-a"))
	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}
	if !exists(paused) || exists(gone) {
		t.Fatalf("paused=%v gone=%v, want true false", exists(paused), exists(gone))
	}
}

// A trim starts through the Go function the engine passed in, so a panic in it is the engine's to handle.
func TestTrimStartsThroughTheEngineGo(t *testing.T) {
	var started atomic.Int32
	f := newStoreBus(t, 100, func(o *Options) {
		o.Go = func(fn func()) { started.Add(1); go fn() }
	})
	f.publish(f.runStart("run-a"), f.runStart("run-b"))
	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("%d trims started for one folder, want 1", n)
	}
}
