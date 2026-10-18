package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// TestRunStartWithoutCommonDirIsRecorded checks a run-start that names no git directory is recorded once in the failures
// file.
func TestRunStartWithoutCommonDirIsRecorded(t *testing.T) {
	f := newFixture(t, 100, nil)
	rs := f.runStart("run-a", "g1")
	rs.Data.(*bus.RunStart).CommonDir = ""
	stop := f.start()
	f.publish(rs)
	stop()
	got := strings.Join(lines(t, filepath.Join(f.homeDir, failuresFile)), "\n")
	if strings.Count(got, `"kind":"no_common_dir"`) != 1 {
		t.Fatalf("%s = %s, want one no_common_dir record", failuresFile, got)
	}
}

// TestRunEndPrunesTheMaps checks run-complete drops the run from the Service's in-memory maps.
func TestRunEndPrunesTheMaps(t *testing.T) {
	f := newFixture(t, 100, nil)
	openRun(t, f, "run-a")
	if _, ok := f.svc.runRepo["run-a"]; !ok {
		t.Fatal("the open run is not tracked")
	}
	f.svc.handle(context.Background(), ev(5, bus.TypeRunComplete, "done", "run-a", "", &bus.RunDone{}))
	if _, ok := f.svc.runRepo["run-a"]; ok {
		t.Fatal("the ended run is still tracked")
	}
}

// TestUnknownRunFindsItsFolderInRunsJSON checks a restarted Service places an event of an earlier run by that folder's
// runs.json.
func TestUnknownRunFindsItsFolderInRunsJSON(t *testing.T) {
	f := newFixture(t, 100, nil)
	openRun(t, f, "run-a")
	f.svc.handle(context.Background(), startRun(1, "run-a", "g1"))
	f.svc.flush()
	again, err := New(f.bus, f.homeDir, Options{RetentionDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	again.handle(context.Background(), ev(9, bus.TypeRunStatus, "task.done", "run-a", "g1", &bus.TaskDone{Task: "late"}))
	again.flush()
	gs, err := NewReader(f.repoDir).Groups(context.Background(), "run-a")
	if err != nil || len(gs) != 1 || gs[0].TasksDone != 1 {
		t.Fatalf("Groups = %+v, %v, want the late task counted in the folder runs.json names", gs, err)
	}
}

// TestEventOfAnUnknownRunIsRecordedOnce checks an event of a run with no known folder gives one no_repository record.
func TestEventOfAnUnknownRunIsRecordedOnce(t *testing.T) {
	f := newFixture(t, 100, nil)
	for range 3 {
		f.svc.handle(context.Background(), ev(9, bus.TypeRunStop, "", "run-zz", "", &bus.RunStop{Target: "x"}))
	}
	got := strings.Join(lines(t, filepath.Join(f.homeDir, failuresFile)), "\n")
	if strings.Count(got, `"kind":"no_repository"`) != 1 {
		t.Fatalf("%s = %s, want one no_repository record", failuresFile, got)
	}
}

// TestRebuildCoversKnownRepositoriesOnAFreshEngine checks Rebuild on a new Service rewrites figures for repositories the
// machine folder lists.
func TestRebuildCoversKnownRepositoriesOnAFreshEngine(t *testing.T) {
	f := newFixture(t, 100, nil)
	stop := f.start()
	f.publish(f.runStart("run-a", "g1"),
		bus.Event{
			Type:    bus.TypeRunStatus,
			Subtype: "run.start",
			Run:     "run-a",
			Data:    &bus.RunStartedFact{Groups: []bus.ID{"g1"}},
		})
	stop()
	for _, n := range []string{"groups.json", "runs.json"} {
		if err := os.Remove(filepath.Join(f.repoDir, n)); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := New(f.bus, f.homeDir, Options{RetentionDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, err := NewReader(f.repoDir).Runs(context.Background())
	if err != nil || len(runs) != 1 || runs[0].Dir != f.repoDir {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
}

// openRun handles a run-start so the Service treats the run as open.
func openRun(t *testing.T, f *fixture, run bus.ID) {
	t.Helper()
	rs := f.runStart(run, "g1")
	rs.Time = t0
	f.svc.handle(context.Background(), rs)
}
