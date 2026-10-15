package bus

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// storeFixture is a bus with an event store over temporary folders.
type storeFixture struct {
	t      *testing.T
	b      Bus
	home   string // the machine events folder
	common string // the repository's git common directory
	repo   string // the repository's events folder
}

// newStoreBus returns a fixture whose bus stores events; opts may adjust the options before New.
func newStoreBus(t *testing.T, buf int, adjust func(*Options)) *storeFixture {
	t.Helper()
	root := t.TempDir()
	f := &storeFixture{t: t, home: filepath.Join(t.TempDir(), "events"), common: filepath.Join(root, ".git")}
	f.repo = EventsDir(f.common)
	o := Options{BufferEvents: buf, EventsDir: f.home}
	if adjust != nil {
		adjust(&o)
	}
	f.b = New(o)
	t.Cleanup(func() { _ = f.b.Close() })
	return f
}

// runStart builds the run-start command of a run in the fixture repository.
func (f *storeFixture) runStart(run ID) Event {
	return Event{Type: TypeRunStart, Run: run, Data: &RunStart{Target: "ep-1", Source: "cli", Repo: "r", Root: filepath.Dir(f.common), CommonDir: f.common}}
}

// publish puts events on the bus and fails the test on a refusal.
func (f *storeFixture) publish(evs ...Event) {
	f.t.Helper()
	for _, e := range evs {
		if err := f.b.Publish(e); err != nil {
			f.t.Fatalf("publish %s/%s: %v", e.Type, e.Subtype, err)
		}
	}
}

// fileLines returns the non-empty lines of a file, or none when it is missing.
func fileLines(t *testing.T, path string) []string {
	t.Helper()
	fh, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if sc.Text() != "" {
			out = append(out, sc.Text())
		}
	}
	return out
}

// runStop builds a run-stop event of a run whose target carries a label.
func runStop(run ID, label string) Event {
	return Event{Type: TypeRunStop, Run: run, Data: &RunStop{Target: label}}
}

// blockLog makes a directory where a run's log file should be, so every append to it fails.
func (f *storeFixture) blockLog(run ID) string {
	f.t.Helper()
	p := filepath.Join(f.repo, LogName(run))
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// A run-start names the git directory; the run's events then go to <common_dir>/komodo/events/<run>.jsonl, one JSON line each.
func TestRunEventsGoToTheRunLog(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"), runStop("run-a", "x"))
	got := fileLines(t, filepath.Join(f.common, "komodo", "events", "run-a.jsonl"))
	if len(got) != 2 || !strings.HasPrefix(got[0], `{"type":"run-start"`) || !strings.Contains(got[1], `"target":"x"`) {
		t.Fatalf("run log = %v", got)
	}
}

// newStoreFixture is newStoreBus with the default queue and options.
func newStoreFixture(t *testing.T) *storeFixture { return newStoreBus(t, 100, nil) }

// An event that names no run goes to engine.jsonl in the machine events folder.
func TestEventWithoutRunGoesToEngineLog(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(Event{Type: TypeRunStop, Data: &RunStop{Target: "ep-1"}})
	got := fileLines(t, filepath.Join(f.home, "engine.jsonl"))
	if len(got) != 1 || !strings.Contains(got[0], `"target":"ep-1"`) {
		t.Fatalf("engine.jsonl = %v", got)
	}
}

// A bus with no events folder stores nothing and still broadcasts.
func TestNoEventsFolderMeansNoStore(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "s")
	stop(t, b, "x")
	if got := recv(t, s).Data.(*RunStop).Target; got != "x" {
		t.Fatalf("got %q", got)
	}
}

// The line is on disk when Publish returns: no buffer sits between the bus and the file.
func TestLineIsWrittenBeforePublishReturns(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	for i, label := range []string{"1", "2", "3"} {
		f.publish(runStop("run-a", label))
		if n := len(fileLines(t, filepath.Join(f.repo, LogName("run-a")))); n != i+2 {
			t.Fatalf("after publish %s the log has %d lines, want %d", label, n, i+2)
		}
	}
}

// Under concurrent publishers the log order equals the order one subscriber saw.
func TestLogOrderEqualsBroadcastOrder(t *testing.T) {
	f := newStoreBus(t, 1000, nil)
	s := sub(t, f.b, "s")
	f.publish(f.runStart("run-a"))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				f.publish(runStop("run-a", string(rune('a'+g))+"-"+strings.Repeat("x", i)))
			}
		}()
	}
	wg.Wait()
	seen := drain(s)
	got := fileLines(t, filepath.Join(f.repo, LogName("run-a")))
	if len(seen) != 201 || len(got) != 201 {
		t.Fatalf("seen %d, logged %d, want 201 each", len(seen), len(got))
	}
	for i, e := range seen {
		var back Event
		if err := json.Unmarshal([]byte(got[i]), &back); err != nil {
			t.Fatal(err)
		}
		if back.Type != e.Type || back.Data.EventKey() != e.Data.EventKey() || back.Time != e.Time {
			t.Fatalf("line %d is %s, subscriber saw %s", i, got[i], e.Type)
		}
		if rs, ok := e.Data.(*RunStop); ok && back.Data.(*RunStop).Target != rs.Target {
			t.Fatalf("line %d target %q, subscriber saw %q", i, back.Data.(*RunStop).Target, rs.Target)
		}
	}
}

// A sealed bus writes nothing more.
func TestSealedBusWritesNothing(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	f.b.Seal()
	if err := f.b.Publish(runStop("run-a", "late")); err == nil {
		t.Fatal("publish after Seal succeeded")
	}
	if n := len(fileLines(t, filepath.Join(f.repo, LogName("run-a")))); n != 1 {
		t.Fatalf("log has %d lines, want 1", n)
	}
}

// The bus.overflow event the bus publishes is logged too; it names no run, so it lands in engine.jsonl.
func TestOverflowEventIsLogged(t *testing.T) {
	f := newStoreBus(t, 2, nil)
	slow := sub(t, f.b, "slow", TypeRunStop)
	e := sub(t, f.b, "errs", TypeError)
	f.publish(f.runStart("run-a"))
	for _, l := range []string{"1", "2", "3"} {
		f.publish(runStop("run-a", l))
	}
	if slow.Dropped() == 0 {
		t.Fatal("no drop happened")
	}
	if got := drain(e); len(got) != 1 || got[0].Subtype != "bus.overflow" {
		t.Fatalf("subscriber saw %+v", got)
	}
	if got := fileLines(t, filepath.Join(f.repo, LogName("run-a"))); len(got) != 4 {
		t.Fatalf("run log = %v, want 4 lines", got)
	}
	if got := fileLines(t, filepath.Join(f.home, "engine.jsonl")); len(got) != 1 || !strings.Contains(got[0], `"subtype":"bus.overflow"`) {
		t.Fatalf("engine.jsonl = %v, want the overflow event", got)
	}
}

// A secret in a line is masked before it is written.
func TestStoredLineIsMasked(t *testing.T) {
	f := newStoreBus(t, 10, func(o *Options) {
		o.Mask = func(s string) string { return strings.ReplaceAll(s, "sk-SECRET", "***") }
	})
	f.publish(f.runStart("run-a"), runStop("run-a", "key sk-SECRET leaked"))
	raw, err := os.ReadFile(filepath.Join(f.repo, LogName("run-a")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-SECRET") || !strings.Contains(string(raw), "key *** leaked") {
		t.Fatalf("log not masked: %s", raw)
	}
}

// The bus keeps the run-to-folder map in runs.json, so a new bus places the events of an earlier run.
func TestRunsJSONPlacesRunsAfterARestart(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	raw, err := os.ReadFile(filepath.Join(f.home, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[ID]string
	if err := json.Unmarshal(raw, &m); err != nil || m["run-a"] != f.repo {
		t.Fatalf("runs.json = %s, %v", raw, err)
	}
	again := New(Options{BufferEvents: 10, EventsDir: f.home})
	t.Cleanup(func() { _ = again.Close() })
	if err := again.Publish(runStop("run-a", "late")); err != nil {
		t.Fatal(err)
	}
	got := fileLines(t, filepath.Join(f.repo, LogName("run-a")))
	if len(got) != 2 || !strings.Contains(got[1], "late") {
		t.Fatalf("log = %v, want the late event appended", got)
	}
}

// Each stage.enter and each final error event is forced to disk; other events are not.
func TestFsyncOnStageEnterAndFinalError(t *testing.T) {
	var mu sync.Mutex
	syncs := 0
	old := syncFile
	syncFile = func(*os.File) error { mu.Lock(); syncs++; mu.Unlock(); return nil }
	t.Cleanup(func() { syncFile = old })
	f := newStoreFixture(t)
	count := func() int { mu.Lock(); defer mu.Unlock(); return syncs }
	f.publish(f.runStart("run-a"), runStop("run-a", "x"))
	if count() != 0 {
		t.Fatalf("%d syncs for ordinary events, want 0", count())
	}
	f.publish(Event{Type: TypeRunStatus, Subtype: "stage.enter", Run: "run-a", Group: "g1", Data: &StageEnter{To: "build"}})
	if count() != 1 {
		t.Fatalf("%d syncs after stage.enter, want 1", count())
	}
	f.publish(Event{Type: TypeError, Subtype: "test.final", Run: "run-a", Data: &finalFault{Final: false}})
	if count() != 1 {
		t.Fatalf("%d syncs after a non-final error, want 1", count())
	}
	f.publish(Event{Type: TypeError, Subtype: "test.final", Run: "run-a", Data: &finalFault{Final: true}})
	if count() != 2 {
		t.Fatalf("%d syncs after a final error, want 2", count())
	}
}

// finalFault stands in for an error event data that carries the final flag, which another package owns.
type finalFault struct {
	Final bool `json:"final"`
}

// EventKey binds the stand-in to a test-only subtype.
func (finalFault) EventKey() Key { return Key{TypeError, "test.final"} }

// init binds the stand-in once for the test binary.
func init() { Register(finalFault{}.EventKey(), func() Payload { return &finalFault{} }) }

// A failed append still broadcasts; the bus publishes one bus.store_failed for the burst, then a gap line when appends work again.
func TestStoreFailureBroadcastsAndMarksAGap(t *testing.T) {
	f := newStoreFixture(t)
	s := sub(t, f.b, "s")
	bad := f.blockLog("run-a")
	f.publish(f.runStart("run-a"))
	for _, l := range []string{"1", "2", "3"} {
		f.publish(runStop("run-a", l))
	}
	seen := drain(s)
	var failed []Event
	stops := 0
	for _, e := range seen {
		switch e.Subtype {
		case "bus.store_failed":
			failed = append(failed, e)
		case "":
			if e.Type == TypeRunStop {
				stops++
			}
		}
	}
	if stops != 3 || len(failed) != 1 {
		t.Fatalf("saw %d run-stop and %d store_failed events, want 3 and 1: %+v", stops, len(failed), seen)
	}
	d := failed[0].Data.(*BusStoreFailed)
	if failed[0].Type != TypeError || d.Run != "run-a" || d.Path != bad || d.Reason == "" {
		t.Fatalf("store_failed = %+v %+v", failed[0], d)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	f.publish(runStop("run-a", "kept"))
	got := fileLines(t, bad)
	if len(got) != 2 || !strings.Contains(got[0], `"gap":true`) || !strings.Contains(got[1], "kept") {
		t.Fatalf("log = %v, want a gap line then the kept event", got)
	}
	var g gapLine
	if err := json.Unmarshal([]byte(got[0]), &g); err != nil || g.Dropped != 5 {
		t.Fatalf("gap = %+v, %v, want 5 lost lines (run-start, 3 stops, store_failed)", g, err)
	}
}

// A second failure burst after appends worked again gets its own bus.store_failed.
func TestEachFailureBurstIsAnnouncedOnce(t *testing.T) {
	f := newStoreFixture(t)
	s := sub(t, f.b, "s", TypeError)
	bad := f.blockLog("run-a")
	f.publish(f.runStart("run-a"), runStop("run-a", "x"), runStop("run-a", "y"))
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	// The run-complete is stored and closes the log, so the next append opens the path again.
	f.publish(runStop("run-a", "ok"), Event{Type: TypeRunComplete, Subtype: "done", Run: "run-a", Data: &RunDone{}})
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	f.blockLog("run-a")
	f.publish(runStop("run-a", "z"), runStop("run-a", "w"))
	n := 0
	for _, e := range drain(s) {
		if e.Subtype == "bus.store_failed" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d bus.store_failed events, want 2", n)
	}
}

// A run-start that names no git directory is a store failure; the event is still broadcast.
func TestRunStartWithoutCommonDirIsAStoreFailure(t *testing.T) {
	f := newStoreFixture(t)
	s := sub(t, f.b, "s")
	rs := f.runStart("run-a")
	rs.Data.(*RunStart).CommonDir = ""
	f.publish(rs)
	got := drain(s)
	if len(got) != 2 || got[0].Type != TypeRunStart || got[1].Subtype != "bus.store_failed" {
		t.Fatalf("saw %+v", got)
	}
	if got[1].Data.(*BusStoreFailed).Run != "run-a" {
		t.Fatalf("store_failed = %+v", got[1].Data)
	}
}

// A run id that cannot be one file name is a store failure, and no file leaves the folder.
func TestUnsafeRunIDIsAStoreFailure(t *testing.T) {
	f := newStoreFixture(t)
	s := sub(t, f.b, "s", TypeError)
	f.publish(f.runStart("../escape"))
	if got := drain(s); len(got) != 1 || got[0].Subtype != "bus.store_failed" {
		t.Fatalf("saw %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.common, "komodo", "escape.jsonl")); err == nil {
		t.Fatal("a log escaped the events folder")
	}
}

// A torn last line left by a killed process is ended before the next line.
func TestTornLineIsEndedBeforeAppend(t *testing.T) {
	f := newStoreFixture(t)
	if err := os.MkdirAll(f.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.repo, LogName("run-a"))
	if err := os.WriteFile(p, []byte(`{"type":"run-st`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.publish(f.runStart("run-a"))
	got := fileLines(t, p)
	if len(got) != 2 || !strings.HasPrefix(got[1], `{"type":"run-start"`) {
		t.Fatalf("log = %v", got)
	}
}

// A run-complete closes the run's file; a later event for that run reopens it.
func TestRunCompleteClosesTheLog(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	mb := f.b.(*memBus)
	if len(mb.logs) != 1 {
		t.Fatalf("%d logs open, want 1", len(mb.logs))
	}
	f.publish(Event{Type: TypeRunComplete, Subtype: "done", Run: "run-a", Data: &RunDone{}})
	if len(mb.logs) != 0 {
		t.Fatalf("%d logs open after run-complete, want 0", len(mb.logs))
	}
	f.publish(runStop("run-a", "again"))
	if n := len(fileLines(t, filepath.Join(f.repo, LogName("run-a")))); n != 3 {
		t.Fatalf("log has %d lines, want 3", n)
	}
}

// Time stamps in the log are the ones Publish stamped.
func TestLogCarriesThePublishTime(t *testing.T) {
	old := now
	fixed := time.Date(2026, 10, 14, 15, 31, 12, 0, time.UTC)
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = old })
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	got := fileLines(t, filepath.Join(f.repo, LogName("run-a")))
	if len(got) != 1 || !strings.Contains(got[0], `"time":"2026-10-14T15:31:12Z"`) {
		t.Fatalf("log = %v", got)
	}
}
