package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// replaceSync swaps syncFile for fn until the test ends.
func replaceSync(t *testing.T, fn func(*os.File) error) {
	t.Helper()
	old := syncFile
	syncFile = fn
	t.Cleanup(func() { syncFile = old })
}

// storeFailures returns the bus.store_failed events among es.
func storeFailures(es []Event) []*BusStoreFailed {
	var out []*BusStoreFailed
	for _, e := range es {
		if d, ok := e.Data.(*BusStoreFailed); ok {
			out = append(out, d)
		}
	}
	return out
}

// stageEnter builds a stage.enter event of run-a.
func stageEnter(run ID) Event {
	return Event{Type: TypeRunStatus, Subtype: "stage.enter", Run: run, Group: "g1", Data: &StageEnter{To: "build"}}
}

// Without an open check every run counts as open, so a trim deletes nothing it cannot prove closed.
func TestTrimWithoutAnOpenCheckDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	old := 40 * 24 * time.Hour
	p := oldLog(t, dir, "run-a", joinLines(jsonLine(t, aged(old, "run-a"))), old)
	res, err := Trim(context.Background(), dir, TrimOptions{})
	if err != nil || exists(p) == false || len(res.Deleted) != 0 {
		t.Fatalf("exists=%v res=%+v err=%v, want the log kept", exists(p), res, err)
	}
	f := newStoreBus(t, 10, func(o *Options) { o.RetentionDays = 1 })
	q := oldLog(t, f.repo, "run-b", joinLines(jsonLine(t, aged(old, "run-b"))), old)
	f.publish(f.runStart("run-c"))
	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}
	if !exists(q) {
		t.Fatal("a bus with no RunOpen deleted a log")
	}
}

// A log written to within the last day is never deleted, whatever its last line says.
func TestTrimSparesALogWrittenInTheLastDay(t *testing.T) {
	dir := t.TempDir()
	p := writeLog(t, dir, "run-a", joinLines(jsonLine(t, aged(40*24*time.Hour, "run-a"))))
	if _, err := Trim(context.Background(), dir, TrimOptions{Open: openRuns()}); err != nil {
		t.Fatal(err)
	}
	if !exists(p) {
		t.Fatal("a log written to just now was deleted")
	}
}

// Events of different unplaceable runs are announced one each; repeats for one run are not.
func TestUnplacedEventsAreAnnouncedPerRun(t *testing.T) {
	f := newStoreFixture(t)
	s := sub(t, f.b, "s", TypeError)
	for _, run := range []ID{"ghost-1", "ghost-1", "ghost-2"} {
		f.publish(runStop(run, "x"))
	}
	got := storeFailures(drain(s))
	if len(got) != 2 {
		t.Fatalf("%d bus.store_failed events, want 2: %+v", len(got), got)
	}
	for i, run := range []string{"ghost-1", "ghost-2"} {
		if got[i].Run != ID(run) || got[i].Path != "" || !strings.Contains(got[i].Reason, run) {
			t.Errorf("event %d = %+v, want run %s, empty path and a reason naming it", i, got[i], run)
		}
	}
}

// A sync that fails after a good write loses no event: no gap line, the log stays open, and one warning per burst.
func TestSyncFailureIsNotALostEvent(t *testing.T) {
	var mu sync.Mutex
	failing := true
	replaceSync(t, func(*os.File) error {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return errors.New("disk full")
		}
		return nil
	})
	f := newStoreFixture(t)
	s := sub(t, f.b, "s", TypeError)
	f.publish(f.runStart("run-a"), stageEnter("run-a"), stageEnter("run-a"))
	got := storeFailures(drain(s))
	if len(got) != 1 || got[0].Reason != "sync: disk full" || got[0].Run != "run-a" {
		t.Fatalf("store_failed = %+v, want one with reason sync: disk full", got)
	}
	if n := len(f.b.(*memBus).logs); n != 1 {
		t.Fatalf("%d logs open, want the log kept open", n)
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	f.publish(stageEnter("run-a"))
	mu.Lock()
	failing = true
	mu.Unlock()
	f.publish(stageEnter("run-a"))
	if n := len(storeFailures(drain(s))); n != 1 {
		t.Fatalf("%d new warnings after the burst ended, want 1", n)
	}
	for _, l := range fileLines(t, filepath.Join(f.repo, LogName("run-a"))) {
		if strings.Contains(l, `"gap":true`) {
			t.Fatalf("a sync failure wrote a gap line: %s", l)
		}
	}
}

// A slow sync does not hold the bus lock: another event publishes while it runs.
func TestSlowSyncDoesNotBlockOtherPublishes(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	replaceSync(t, func(*os.File) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.b.Publish(stageEnter("run-a"))
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the sync never started")
	}
	other := make(chan error, 1)
	go func() { other <- f.b.Publish(runStop("run-a", "meanwhile")) }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a publish waited for another event's sync")
	}
	close(release)
	<-done
}

// A corrupt runs.json is moved aside, the bus starts empty and publishes one warning; later saves keep rows they did not
// know.
func TestCorruptRunsJSONIsMovedAside(t *testing.T) {
	f := newStoreFixture(t)
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.home, "runs.json")
	if err := os.WriteFile(path, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := New(Options{BufferEvents: 10, EventsDir: f.home})
	t.Cleanup(func() { _ = b.Close() })
	moved, _ := filepath.Glob(path + ".bad-*")
	if len(moved) != 1 {
		t.Fatalf("moved files %v, want one runs.json.bad-<unix>", moved)
	}
	if got := fileLines(t, filepath.Join(f.home, "engine.jsonl")); len(got) != 1 ||
		!strings.Contains(got[0], `"subtype":"bus.store_failed"`) {
		t.Fatalf("engine.jsonl = %v, want one bus.store_failed", got)
	}
	// A row another writer added after the start survives this bus's save.
	if err := os.WriteFile(path, []byte(`{"other-run":"/elsewhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(f.runStart("run-a")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var m map[ID]string
	if err := json.Unmarshal(raw, &m); err != nil || m["other-run"] != "/elsewhere" || m["run-a"] != f.repo {
		t.Fatalf("runs.json = %s, %v", raw, err)
	}
}

// A runs.json that cannot be saved is announced once per burst, and again after a save that works.
func TestRunsJSONSaveFailuresAreAnnouncedPerBurst(t *testing.T) {
	f := newStoreFixture(t)
	s := sub(t, f.b, "s", TypeError)
	path := filepath.Join(f.home, "runs.json")
	block := func() {
		_ = os.Remove(path)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	block()
	f.publish(f.runStart("run-a"), f.runStart("run-b"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	f.publish(f.runStart("run-c"))
	block()
	f.publish(f.runStart("run-d"))
	n := 0
	for _, d := range storeFailures(drain(s)) {
		if strings.HasSuffix(d.Path, "runs.json") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d runs.json warnings, want 2", n)
	}
}

// engine.jsonl is trimmed by the daily trigger: lines past retention go, and the oldest go first past the size bound.
func TestMachineLogIsTrimmed(t *testing.T) {
	line := func(d time.Duration, n int) string {
		return fmt.Sprintf(
			`{"type":"run-stop","subtype":"","run":"","group":"","time":%q,"data":{"target":"l%d"}}`,
			time.Now().UTC().Add(-d).Format(time.RFC3339),
			n,
		)
	}
	f := newStoreBus(t, 10, func(o *Options) { o.RetentionDays = 30 })
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.home, "engine.jsonl")
	if err := os.WriteFile(
		p,
		[]byte(joinLines(line(40*24*time.Hour, 1), line(35*24*time.Hour, 2), line(time.Hour, 3))),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	f.publish(f.runStart("run-a"))
	f.publish(Event{Type: TypeRunStop, Data: &RunStop{Target: "after"}})
	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}
	got := fileLines(t, p)
	if len(got) != 2 || !strings.Contains(got[0], `"l3"`) || !strings.Contains(got[1], `"after"`) {
		t.Fatalf("engine.jsonl = %v, want the recent line and the one published after", got)
	}

	old := sizeBound
	sizeBound = 500
	t.Cleanup(func() { sizeBound = old })
	g := newStoreBus(t, 10, nil)
	if err := os.MkdirAll(g.home, 0o755); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(g.home, "engine.jsonl")
	var ls []string
	for i := range 10 {
		ls = append(ls, line(time.Hour, i))
	}
	if err := os.WriteFile(q, []byte(joinLines(ls...)), 0o644); err != nil {
		t.Fatal(err)
	}
	g.publish(g.runStart("run-a"))
	if err := g.b.Close(); err != nil {
		t.Fatal(err)
	}
	left := fileLines(t, q)
	if len(left) == 0 || len(left) >= 10 || !strings.Contains(left[len(left)-1], `"l9"`) {
		t.Fatalf("engine.jsonl holds %d lines, want fewer than 10 and the newest kept: %v", len(left), left)
	}
	st, _ := os.Stat(q)
	if st.Size() > 500 {
		t.Fatalf("engine.jsonl is %d bytes, want at most the bound", st.Size())
	}
}

// A final error event syncs every open log, not only its own.
func TestFinalErrorSyncsEveryOpenLog(t *testing.T) {
	var mu sync.Mutex
	var names []string
	replaceSync(t, func(f *os.File) error {
		mu.Lock()
		names = append(names, filepath.Base(f.Name()))
		mu.Unlock()
		return nil
	})
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"), f.runStart("run-b"), Event{Type: TypeRunStop, Data: &RunStop{Target: "x"}})
	f.publish(Event{Type: TypeError, Subtype: "test.final", Run: "run-a", Data: &finalFault{Final: true}})
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"run-a.jsonl": true, "run-b.jsonl": true, "engine.jsonl": true}
	if len(names) != len(want) {
		t.Fatalf("synced %v, want %d logs", names, len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("synced %s unexpectedly", n)
		}
	}
}

// A stalled run-failure closes the run's log, like a run-complete.
func TestStalledRunFailureClosesTheLog(t *testing.T) {
	f := newStoreFixture(t)
	f.publish(f.runStart("run-a"))
	mb := f.b.(*memBus)
	mb.mu.Lock()
	mb.storeLocked(Event{Type: TypeRunFailure, Subtype: "stalled", Run: "run-a", Time: t0, Data: &RunStop{}})
	n := len(mb.logs)
	mb.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d logs open after a stalled run, want 0", n)
	}
}

// At most 64 logs stay open; the least recently written is closed and reopened when its run writes again.
func TestOpenLogsAreCapped(t *testing.T) {
	f := newStoreBus(t, 10, nil)
	mb := f.b.(*memBus)
	const runs = 70
	for i := range runs {
		f.publish(f.runStart(ID(fmt.Sprintf("run-%02d", i))))
	}
	for i := range runs {
		f.publish(runStop(ID(fmt.Sprintf("run-%02d", i)), "again"))
	}
	mb.mu.Lock()
	open := len(mb.logs)
	mb.mu.Unlock()
	if open > maxOpenLogs || maxOpenLogs != 64 {
		t.Fatalf("%d logs open, cap %d, want at most 64", open, maxOpenLogs)
	}
	for i := range runs {
		if n := len(fileLines(t, filepath.Join(f.repo, LogName(ID(fmt.Sprintf("run-%02d", i)))))); n != 2 {
			t.Fatalf("run-%02d has %d lines, want 2", i, n)
		}
	}
}

// The final flag is read through the Finaler interface the data implements.
func TestFinalFaultIsAFinaler(t *testing.T) {
	var p Payload = &finalFault{Final: true}
	if f, ok := p.(Finaler); !ok || !f.IsFinal() {
		t.Fatal("finalFault is not a final Finaler")
	}
}
