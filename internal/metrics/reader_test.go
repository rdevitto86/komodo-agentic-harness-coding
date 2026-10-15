package metrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// doneEvent builds a task.done event for a group.
func doneEvent(sec int, group bus.ID, task string) bus.Event {
	return ev(sec, bus.TypeRunStatus, "task.done", "run-a", group, &bus.TaskDone{Task: task})
}

// TestMissingFilesReadAsEmpty checks a read of a folder with no files returns no figures and no error.
func TestMissingFilesReadAsEmpty(t *testing.T) {
	r := NewReader(RepoDir(t.TempDir()))
	runs, err := r.Runs(context.Background())
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs = %v, %v", runs, err)
	}
	gs, err := r.Groups(context.Background(), "run-a")
	if err != nil || len(gs) != 0 {
		t.Fatalf("Groups = %v, %v", gs, err)
	}
}

// TestCorruptFiguresAreRebuilt checks a corrupt groups.json and runs.json read as figures rebuilt from the bus store.
func TestCorruptFiguresAreRebuilt(t *testing.T) {
	common := t.TempDir()
	dir := RepoDir(common)
	writeLog(t, bus.EventsDir(common), "run-a", joinLines(
		jsonLine(t, startRun(0, "run-a", "g1", "g2")),
		jsonLine(t, enter(0, "run-a", "g1", "build", false)),
		jsonLine(t, enter(40, "run-a", "g1", "review", false)),
	))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
