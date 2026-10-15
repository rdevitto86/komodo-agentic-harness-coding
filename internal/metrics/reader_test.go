package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// doneEvent builds a task.done event for a group.
func doneEvent(sec int, group bus.ID, task string) bus.Event {
	return ev(sec, bus.TypeRunStatus, "task.done", "run-a", group, &bus.TaskDone{Task: task})
}

// TestReplayTornLastLine checks a last line with no closing newline is dropped and marked torn.
func TestReplayTornLastLine(t *testing.T) {
	dir := t.TempDir()
	e1, e2 := doneEvent(1, "g1", "a"), doneEvent(2, "g1", "b")
	torn := jsonLine(t, doneEvent(3, "g1", "c"))[:60]
	writeLog(t, dir, "run-a", jsonLine(t, e1)+"\n"+jsonLine(t, e2)+"\n"+torn)
	got, err := NewReader(dir).Replay(context.Background(), "run-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || len(got.Gaps) != 1 || got.Gaps[0].Kind != "torn" || got.Gaps[0].After != 2 || got.Skipped != 0 {
		t.Fatalf("replay = %+v", got)
	}
}

// TestReplaySkipsUnparseableMiddleLine checks an unparseable line before the last is skipped and counted.
func TestReplaySkipsUnparseableMiddleLine(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "run-a", joinLines(jsonLine(t, doneEvent(1, "g1", "a")), "{not json", jsonLine(t, doneEvent(2, "g1", "b"))))
	got, err := NewReader(dir).Replay(context.Background(), "run-a", "")
	if err != nil || len(got.Events) != 2 || got.Skipped != 1 || len(got.Gaps) != 0 {
		t.Fatalf("replay = %+v, %v", got, err)
	}
}

// TestReplayGroupKeepsGaps checks a group replay returns only that group's events and every gap mark.
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
	got, err := NewReader(dir).Replay(context.Background(), "run-a", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || len(got.Gaps) != 1 {
		t.Fatalf("replay = %+v", got)
	}
	if g := got.Gaps[0]; g.Kind != "dropped" || g.Dropped != 12 || g.After != 1 || g.Time.IsZero() {
		t.Fatalf("gap = %+v", g)
	}
	all, _ := NewReader(dir).Replay(context.Background(), "run-a", "")
	if len(all.Events) != 4 || all.Gaps[0].After != 2 {
		t.Fatalf("whole replay = %+v", all)
	}
}

// TestReplayMissingLog checks a missing log gives an error naming the run.
func TestReplayMissingLog(t *testing.T) {
	_, err := NewReader(t.TempDir()).Replay(context.Background(), "run-zz", "")
	if err == nil || !strings.Contains(err.Error(), "run-zz") {
		t.Fatalf("err = %v, want one naming the run", err)
	}
}

// TestReplayReadsWhatTheServiceWrote checks the reader decodes the service's own lines.
func TestReplayReadsWhatTheServiceWrote(t *testing.T) {
	f := newFixture(t, 100, nil)
	stop := f.start()
	f.publish(f.runStart("run-a", "g1"), bus.Event{Type: bus.TypeRunStop, Run: "run-a", Data: &bus.RunStop{Target: "x"}})
	stop()
	got, err := NewReader(f.repoDir).Replay(context.Background(), "run-a", "")
	if err != nil || len(got.Events) != 2 || got.Events[1].Data.(*bus.RunStop).Target != "x" {
		t.Fatalf("replay = %+v, %v", got, err)
	}
}

// TestExplainReturnsLastEvents checks Explain returns the last n event copies in order.
func TestExplainReturnsLastEvents(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "run-a", joinLines(
		jsonLine(t, doneEvent(1, "g1", "a")), jsonLine(t, doneEvent(2, "g1", "b")), jsonLine(t, doneEvent(3, "g1", "c"))))
	got, err := NewReader(dir).Explain(context.Background(), "run-a", 2)
	if err != nil || len(got) != 2 || got[0].Data.(*bus.TaskDone).Task != "b" || got[1].Data.(*bus.TaskDone).Task != "c" {
		t.Fatalf("explain = %+v, %v", got, err)
	}
}

// TestMissingFilesReadAsEmpty checks a read of a folder with no files returns no figures and no error.
func TestMissingFilesReadAsEmpty(t *testing.T) {
	r := NewReader(filepath.Join(t.TempDir(), "none"))
	runs, err := r.Runs(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs = %v, %v", runs, err)
	}
	gs, err := r.Groups(context.Background(), "run-a")
	if err != nil || len(gs) != 0 {
		t.Fatalf("Groups = %v, %v", gs, err)
	}
}

// TestCorruptFiguresAreRebuilt checks a corrupt groups.json and runs.json read as figures rebuilt from the run logs.
func TestCorruptFiguresAreRebuilt(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "run-a", joinLines(
		jsonLine(t, startRun(0, "run-a", "g1", "g2")),
		jsonLine(t, enter(0, "run-a", "g1", "build", false)),
		jsonLine(t, enter(40, "run-a", "g1", "review", false)),
	))
	for _, n := range []string{"groups.json", "runs.json"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{corrupt"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReader(dir)
	gs, err := r.Groups(context.Background(), "run-a")
	if err != nil || len(gs) != 2 {
		t.Fatalf("Groups = %+v, %v", gs, err)
	}
	for _, g := range gs {
		if g.Group == "g1" && stageOf(t, g, "build").Seconds != 40 {
			t.Fatalf("g1 = %+v, want build 40 s", g)
		}
	}
	runs, err := r.Runs(context.Background())
	if err != nil || len(runs) != 1 || runs[0].Groups != 2 {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "groups.json")); string(b) != "{corrupt" {
		t.Fatalf("a read must not write; groups.json is now %q", b)
	}
}
