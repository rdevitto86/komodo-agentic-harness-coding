package bus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// t0 is the fixed start time of the hand-built events.
var t0 = time.Date(2026, 10, 14, 15, 20, 0, 0, time.UTC)

// doneEvent builds a task.done event of run-a, sec seconds after t0.
func doneEvent(sec int, group ID, task string) Event {
	return Event{Type: TypeRunStatus, Subtype: "task.done", Run: "run-a", Group: group, Time: t0.Add(time.Duration(sec) * time.Second), Data: &TaskDone{Task: task}}
}

// jsonLine marshals an event as one log line.
func jsonLine(t *testing.T, e Event) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeLog writes raw content to a run's log in dir and returns its path.
func writeLog(t *testing.T, dir string, run ID, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, LogName(run))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// joinLines joins lines with newlines and a closing newline.
func joinLines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// writeRuns writes a machine folder's runs.json from the map and returns the folder.
func writeRuns(t *testing.T, m map[ID]string) string {
	t.Helper()
	home := t.TempDir()
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(home, "runs.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// readerOver returns a Reader whose runs.json places run-a in dir.
func readerOver(t *testing.T, dir string) Reader {
	t.Helper()
	return NewReader(writeRuns(t, map[ID]string{"run-a": dir}))
}

// A last line with no closing newline is dropped and marked torn.
func TestReplayTornLastLine(t *testing.T) {
	dir := t.TempDir()
	torn := jsonLine(t, doneEvent(3, "g1", "c"))[:60]
	writeLog(t, dir, "run-a", jsonLine(t, doneEvent(1, "g1", "a"))+"\n"+jsonLine(t, doneEvent(2, "g1", "b"))+"\n"+torn)
	got, err := readerOver(t, dir).Replay(context.Background(), "run-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || len(got.Gaps) != 1 || got.Gaps[0].Kind != "torn" || got.Gaps[0].After != 2 || got.Skipped != 0 {
		t.Fatalf("replay = %+v", got)
	}
}

// An unparseable line before the last is skipped and counted.
func TestReplaySkipsUnparseableMiddleLine(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "run-a", joinLines(jsonLine(t, doneEvent(1, "g1", "a")), "{not json", jsonLine(t, doneEvent(2, "g1", "b"))))
	got, err := readerOver(t, dir).Replay(context.Background(), "run-a", "")
	if err != nil || len(got.Events) != 2 || got.Skipped != 1 || len(got.Gaps) != 0 {
		t.Fatalf("replay = %+v, %v", got, err)
	}
}

// A group replay returns only that group's events and every gap mark.
func TestReplayGroupKeepsGaps(t *testing.T) {
	dir := t.TempDir()
	gap := `{"gap":true,"time":"2026-10-14T15:40:11Z","dropped":12}`
	writeLog(t, dir, "run-a", joinLines(
		jsonLine(t, doneEvent(1, "g1", "a")),
		jsonLine(t, doneEvent(2, "g2", "b")),
		gap,
		jsonLine(t, doneEvent(3, "g2", "c")),
		jsonLine(t, doneEvent(4, "g1", "d")),
	))
	r := readerOver(t, dir)
	got, err := r.Replay(context.Background(), "run-a", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || len(got.Gaps) != 1 {
		t.Fatalf("replay = %+v", got)
	}
	if g := got.Gaps[0]; g.Kind != "dropped" || g.Dropped != 12 || g.After != 1 || g.Time.IsZero() {
		t.Fatalf("gap = %+v", g)
	}
	all, _ := r.Replay(context.Background(), "run-a", "")
	if len(all.Events) != 4 || all.Gaps[0].After != 2 {
		t.Fatalf("whole replay = %+v", all)
	}
}

// A run that runs.json does not list, and a listed run with no log file, each give an error naming the run.
func TestReplayMissingLog(t *testing.T) {
	cases := map[string]struct {
		r   Reader
		run ID
	}{
		"not listed": {NewReader(t.TempDir()), "run-zz"},
		"no file":    {readerOver(t, t.TempDir()), "run-a"},
	}
	for name, c := range cases {
		if _, err := c.r.Replay(context.Background(), c.run, ""); err == nil || !strings.Contains(err.Error(), string(c.run)) {
			t.Errorf("%s: err = %v, want one naming the run", name, err)
		}
	}
}

// A run id that is not one path segment is refused.
func TestReplayRefusesAnUnsafeRunID(t *testing.T) {
	if _, err := NewReader(t.TempDir()).Replay(context.Background(), "../x", ""); err == nil {
		t.Fatal("want an error")
	}
}

// The reader decodes the lines the bus itself wrote.
func TestReplayReadsWhatTheBusWrote(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"), runStop("run-a", "x"))
	got, err := NewReader(f.home).Replay(context.Background(), "run-a", "")
	if err != nil || len(got.Events) != 2 || got.Events[1].Data.(*RunStop).Target != "x" {
		t.Fatalf("replay = %+v, %v", got, err)
	}
}

// Explain returns the last n event copies in order.
func TestExplainReturnsLastEvents(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "run-a", joinLines(
		jsonLine(t, doneEvent(1, "g1", "a")), jsonLine(t, doneEvent(2, "g1", "b")), jsonLine(t, doneEvent(3, "g1", "c"))))
	got, err := readerOver(t, dir).Explain(context.Background(), "run-a", 2)
	if err != nil || len(got) != 2 || got[0].Data.(*TaskDone).Task != "b" || got[1].Data.(*TaskDone).Task != "c" {
		t.Fatalf("explain = %+v, %v", got, err)
	}
}

// ReadFolder hands over each run's log in file-name order, skips other files, and reads a missing folder as empty.
func TestReadFolderVisitsEveryRunLog(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "run-b", joinLines(jsonLine(t, doneEvent(1, "g1", "b"))))
	writeLog(t, dir, "run-a", joinLines(jsonLine(t, doneEvent(1, "g1", "a")), jsonLine(t, doneEvent(2, "g1", "a2"))))
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var runs []ID
	var n int
	err := ReadFolder(context.Background(), dir, func(run ID, l ReplayLog) error {
		runs = append(runs, run)
		n += len(l.Events)
		return nil
	})
	if err != nil || len(runs) != 2 || runs[0] != "run-a" || runs[1] != "run-b" || n != 3 {
		t.Fatalf("runs %v events %d err %v", runs, n, err)
	}
	if err := ReadFolder(context.Background(), filepath.Join(dir, "none"), func(ID, ReplayLog) error { return nil }); err != nil {
		t.Fatalf("a missing folder reads as empty, got %v", err)
	}
}
