package state

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Create writes rev 1 with a filled header, and Load returns the same record.
func TestCreateAndLoadRoundTrip(t *testing.T) {
	m, _ := newMgr(t)
	r := mustCreate(t, m)
	h := r.Header
	if h.V != 1 || h.Rev != 1 || h.Group != testGroup || h.Writer != testWriter {
		t.Fatalf("header: %+v", h)
	}
	if h.TS.Location() != time.UTC || h.TS.Nanosecond() != 0 {
		t.Fatalf("ts must be UTC to the second: %v", h.TS)
	}
	got, err := m.Load(testGroup)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("Load: %+v %v\nwant %+v", got, err, r)
	}
}

// A writer given as a bare component gets run and process added to the header.
func TestBareWriterGetsRunAndPid(t *testing.T) {
	root := t.TempDir()
	m, err := New(root, "engine")
	if err != nil {
		t.Fatal(err)
	}
	r := mustCreate(t, m)
	parts := strings.Split(r.Header.Writer, "/")
	if len(parts) != 3 || parts[0] != "engine" || parts[1] != testRun {
		t.Fatalf("writer: %q", r.Header.Writer)
	}
}

// A refused write names the failing field path and leaves the file byte-identical.
func TestInvalidWriteKeepsFileBytes(t *testing.T) {
	m, root := newMgr(t)
	r := mustCreate(t, m)
	before := readBytes(t, statePath(root, testGroup))
	cases := map[string]func(g *GroupState){
		"state.run":                       func(g *GroupState) { g.Run = "" },
		"state.stage":                     func(g *GroupState) { g.Stage = "nowhere" },
		"state.config_hash":               func(g *GroupState) { g.ConfigHash = "abc" },
		"state.tasks[0].state":            func(g *GroupState) { g.Tasks[0].State = "lost" },
		"state.test_review.tester.result": func(g *GroupState) { g.TestReview.Tester.Result = "maybe" },
		"state.block.reason":              func(g *GroupState) { g.Block.Reason = "bad_luck" },
		"state.prior":                     func(g *GroupState) { g.Prior = StageBuild },
		"state.outcome":                   func(g *GroupState) { g.Outcome = "complete" },
		"state.group":                     func(g *GroupState) { g.Group = "other" },
	}
	for field, mut := range cases {
		g := goodState()
		mut(&g)
		_, err := m.Save(testGroup, g, r.Header.Rev)
		wantInvalid(t, err, field)
		if !bytes.Equal(before, readBytes(t, statePath(root, testGroup))) {
			t.Fatalf("%s: file changed", field)
		}
	}
}

// A done group needs an outcome.
func TestDoneNeedsOutcome(t *testing.T) {
	m, _ := newMgr(t)
	r := mustCreate(t, m)
	g := goodState()
	g.Stage = StageDone
	_, err := m.Save(testGroup, g, r.Header.Rev)
	wantInvalid(t, err, "state.outcome")
	g.Outcome = "complete"
	if _, err := m.Save(testGroup, g, r.Header.Rev); err != nil {
		t.Fatal(err)
	}
}

// A stale rev is refused and a good one moves the rev up by 1.
func TestStaleRev(t *testing.T) {
	m, _ := newMgr(t)
	r := mustCreate(t, m)
	r2, err := m.Save(testGroup, goodState(), r.Header.Rev)
	if err != nil || r2.Header.Rev != 2 {
		t.Fatalf("Save: %+v %v", r2.Header, err)
	}
	if _, err := m.Save(testGroup, goodState(), r.Header.Rev); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("want ErrStaleRev, got %v", err)
	}
}

// Create refuses a zero fence and an existing record.
func TestCreateRefusals(t *testing.T) {
	m, root := newMgr(t)
	g := goodState()
	g.Fence = 0
	_, err := m.Create(testGroup, g)
	wantInvalid(t, err, "state.fence")
	if _, err := os.Stat(statePath(root, testGroup)); !os.IsNotExist(err) {
		t.Fatal("a refused Create wrote a file")
	}
	mustCreate(t, m)
	if _, err := m.Create(testGroup, goodState()); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

// A Save that lowers the fence or a counter is refused.
func TestMonotoneFenceAndCounters(t *testing.T) {
	m, _ := newMgr(t)
	g := goodState()
	g.Counters.Blocks = map[string]int{"returns": 2}
	r, err := m.Create(testGroup, g)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(g *GroupState){
		"state.fence":                func(g *GroupState) { g.Fence = 2 },
		"state.counters.returns":     func(g *GroupState) { g.Counters.Returns = 0 },
		"state.counters.attempts":    func(g *GroupState) { g.Counters.Attempts = 0 },
		"state.counters.retries":     func(g *GroupState) { g.Counters.Retries = -1 },
		"state.counters.crashes":     func(g *GroupState) { g.Counters.Crashes = -1 },
		"state.counters.wall_used_s": func(g *GroupState) { g.Counters.WallUsedS = 1 },
		"state.counters.tokens_used": func(g *GroupState) { g.Counters.TokensUsed = -1 },
		"state.counters.blocks.returns": func(g *GroupState) {
			g.Counters.Blocks = map[string]int{"returns": 1}
		},
	}
	for field, mut := range cases {
		g := goodState()
		g.Counters.Blocks = map[string]int{"returns": 2}
		mut(&g)
		_, err := m.Save(testGroup, g, r.Header.Rev)
		wantInvalid(t, err, field)
	}
	g = goodState()
	g.Fence = 5
	g.Counters.Blocks = map[string]int{"returns": 3, "wall": 1}
	if _, err := m.Save(testGroup, g, r.Header.Rev); err != nil {
		t.Fatalf("raising fence and counters: %v", err)
	}
}

// Saving a crash adds to crashes and leaves attempts and retries as they were.
func TestCrashCounter(t *testing.T) {
	m, _ := newMgr(t)
	r := mustCreate(t, m)
	g := goodState()
	g.Counters.Crashes = 1
	if _, err := m.Save(testGroup, g, r.Header.Rev); err != nil {
		t.Fatal(err)
	}
	c, err := m.Counters(testGroup)
	if err != nil || c.Crashes != 1 || c.Attempts != 1 || c.Retries != 0 {
		t.Fatalf("Counters: %+v %v", c, err)
	}
}

// Only the engine may write; every write call checks.
func TestNotWriter(t *testing.T) {
	m, root := newMgr(t)
	r := mustCreate(t, m)
	cli, err := New(root, "cli/1")
	if err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, statePath(root, testGroup))
	if _, err := cli.Save(testGroup, goodState(), r.Header.Rev); !errors.Is(err, ErrNotWriter) {
		t.Fatalf("Save: %v", err)
	}
	if _, err := cli.Create("other", goodState()); !errors.Is(err, ErrNotWriter) {
		t.Fatalf("Create: %v", err)
	}
	if err := cli.Remove(testGroup, r.Header.Rev); !errors.Is(err, ErrNotWriter) {
		t.Fatalf("Remove: %v", err)
	}
	if !bytes.Equal(before, readBytes(t, statePath(root, testGroup))) {
		t.Fatal("a refused write changed the file")
	}
	if got, err := cli.Load(testGroup); err != nil || got.Header.Rev != 1 {
		t.Fatalf("a non-writer may read: %v", err)
	}
}

// A nil slice or map in a write is refused; an absent value on disk reads back as [] or {}.
func TestNilRefusedAbsentDecoded(t *testing.T) {
	m, root := newMgr(t)
	g := goodState()
	g.Tasks = nil
	_, err := m.Create(testGroup, g)
	wantInvalid(t, err, "state.tasks")
	g = goodState()
	g.Counters.Blocks = nil
	_, err = m.Create(testGroup, g)
	wantInvalid(t, err, "state.counters.blocks")
	g = goodState()
	g.TestReview.Findings = nil
	_, err = m.Create(testGroup, g)
	wantInvalid(t, err, "state.test_review.findings")

	writeRaw(t, statePath(root, "g2"), `{"v":1,"group":"g2","rev":1,"ts":"2026-10-14T16:01:40Z","writer":"engine/r/1",
	 "state":{"run":"r","group":"g2","stage":"build","fence":1,"config_hash":"`+testHash+`","counters":{"returns":0},"tasks":null}}`)
	r, err := m.Load("g2")
	if err != nil {
		t.Fatal(err)
	}
	s := r.State
	if s.Tasks == nil || s.Owed == nil || s.Requests == nil || s.ScopeAdded == nil || s.Resume == nil ||
		s.ActionsOpen == nil || s.Counters.Blocks == nil || s.TestReview.Findings == nil || s.TestReview.NotMet == nil || s.TestReview.Failed == nil {
		t.Fatalf("absent values must decode to empty: %+v", s)
	}
}

// requests keeps the 64 newest ids.
func TestRequestsTrimmed(t *testing.T) {
	m, _ := newMgr(t)
	g := goodState()
	for i := 0; i < 70; i++ {
		g.Requests = append(g.Requests, "req-"+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	r, err := m.Create(testGroup, g)
	if err != nil || len(r.State.Requests) != 64 || r.State.Requests[63] != g.Requests[69] || r.State.Requests[0] != g.Requests[6] {
		t.Fatalf("Requests: %d %v", len(r.State.Requests), err)
	}
}

// An open action with no arguments is stored with an empty object.
func TestOpenActionArgsNormalised(t *testing.T) {
	m, _ := newMgr(t)
	g := goodState()
	g.ActionsOpen = []OpenAction{{ID: "a1", Kind: "worktree"}}
	r, err := m.Create(testGroup, g)
	if err != nil || string(r.State.ActionsOpen[0].Args) != "{}" {
		t.Fatalf("args: %q %v", r.State.ActionsOpen[0].Args, err)
	}
	g.ActionsOpen = []OpenAction{{ID: "a1", Kind: "worktree", Args: []byte("{bad")}}
	_, err = m.Save(testGroup, g, 1)
	wantInvalid(t, err, "state.actions_open[0].args")
}

// A damaged record is renamed by the writer's Load; Peek and a non-writer leave it.
func TestCorruptRecordHandling(t *testing.T) {
	m, root := newMgr(t)
	p := statePath(root, testGroup)
	writeRaw(t, p, `{"v":1,"group":`)
	if _, err := m.Peek(testGroup); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Peek: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("Peek moved the file")
	}
	other, _ := New(root, "cli/1")
	if _, err := other.Load(testGroup); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("non-writer Load: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a non-writer Load moved the file")
	}
	if _, err := m.Load(testGroup); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("Load left the damaged file in place")
	}
	bad, err := m.Quarantined()
	if err != nil || len(bad) != 1 || !strings.Contains(filepath.Base(bad[0]), "state.json.bad-") {
		t.Fatalf("Quarantined: %v %v", bad, err)
	}
	if _, err := m.Load(testGroup); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after quarantine: %v", err)
	}
}

// Each kind of damage is a failed read check: unknown field, wrong group, bad enum, zero fence.
func TestReadChecks(t *testing.T) {
	m, root := newMgr(t)
	hdr := `"v":1,"group":"` + testGroup + `","rev":1,"ts":"2026-10-14T16:01:40Z","writer":"engine/r/1"`
	st := func(extra string) string {
		return `{` + hdr + `,"state":{"run":"r","group":"` + testGroup + `","stage":"build","fence":1,"config_hash":"` + testHash + `"` + extra + `}}`
	}
	cases := map[string]string{
		"unknown field": st(`,"surprise":1`),
		"bad stage":     strings.Replace(st(""), `"build"`, `"flying"`, 1),
		"zero fence":    strings.Replace(st(""), `"fence":1`, `"fence":0`, 1),
		"wrong group":   strings.Replace(st(""), `"group":"`+testGroup+`","stage"`, `"group":"elsewhere","stage"`, 1),
		"folder name":   strings.Replace(st(""), `"group":"`+testGroup+`","rev"`, `"group":"elsewhere","rev"`, 1),
	}
	for name, body := range cases {
		writeRaw(t, statePath(root, testGroup), body)
		if _, err := m.Peek(testGroup); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: Peek %v", name, err)
		}
	}
	writeRaw(t, statePath(root, testGroup), st(""))
	if _, err := m.Peek(testGroup); err != nil {
		t.Fatalf("valid record: %v", err)
	}
}

// List returns every good record with the first error and writes nothing.
func TestListGoodRecordsAndFirstError(t *testing.T) {
	m, root := newMgr(t)
	mustCreate(t, m)
	g := goodState()
	g.Group = "g-b"
	if _, err := m.Create("g-b", g); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, statePath(root, "g-c"), `not json`)
	recs, err := m.List()
	if !errors.Is(err, ErrCorrupt) || len(recs) != 2 {
		t.Fatalf("List: %d records, %v", len(recs), err)
	}
	if _, err := os.Stat(statePath(root, "g-c")); err != nil {
		t.Fatal("List moved the damaged file")
	}
	if recs[0].Header.Group > recs[1].Header.Group {
		t.Fatal("List is not sorted by group")
	}
}

// List over a missing folder is empty.
func TestListEmpty(t *testing.T) {
	m, _ := newMgr(t)
	if recs, err := m.List(); err != nil || len(recs) != 0 {
		t.Fatalf("List: %v %v", recs, err)
	}
}

// A killed write leaves the old record readable and a stale temporary file that the next open removes.
func TestStaleTempRemovedOnOpen(t *testing.T) {
	m, root := newMgr(t)
	mustCreate(t, m)
	dir := filepath.Dir(statePath(root, testGroup))
	stale := filepath.Join(dir, ".state.json.tmp-111")
	fresh := filepath.Join(dir, ".state.json.tmp-222")
	writeRaw(t, stale, "half")
	writeRaw(t, fresh, "half")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Load(testGroup); err != nil || got.Header.Rev != 1 {
		t.Fatalf("old record unreadable: %v", err)
	}
	if _, err := New(root, testWriter); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale temporary file kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh temporary file removed")
	}
}

// Of two goroutines saving with the same rev, exactly one succeeds.
func TestConcurrentSavesOneWins(t *testing.T) {
	m, _ := newMgr(t)
	r := mustCreate(t, m)
	var wins, stale atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Save(testGroup, goodState(), r.Header.Rev)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrStaleRev):
				stale.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || stale.Load() != 7 {
		t.Fatalf("wins %d stale %d", wins.Load(), stale.Load())
	}
}

// The first Save accepts the fence the Lease Manager granted, as a plain number.
func TestFenceFromLease(t *testing.T) {
	m, _ := newMgr(t)
	g := goodState()
	g.Fence = 7
	r, err := m.Create(testGroup, g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(testGroup, g, r.Header.Rev); err != nil {
		t.Fatalf("first Save with the lease fence: %v", err)
	}
}

// Counters reads only the counters; the rest of the record may be unreadable to it.
func TestCountersReadsOnlyCounters(t *testing.T) {
	m, root := newMgr(t)
	writeRaw(t, statePath(root, testGroup), `{"v":1,"state":{"counters":{"returns":2,"blocks":null},"tasks":"not a list"}}`)
	c, err := m.Counters(testGroup)
	if err != nil || c.Returns != 2 || c.Blocks == nil {
		t.Fatalf("Counters: %+v %v", c, err)
	}
	if _, err := m.Counters("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent: %v", err)
	}
	writeRaw(t, statePath(root, "newer"), `{"v":9,"state":{}}`)
	if _, err := m.Counters("newer"); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("newer: %v", err)
	}
	writeRaw(t, statePath(root, "broken"), `{`)
	if _, err := m.Counters("broken"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("broken: %v", err)
	}
}

// After a simulated crash a fresh manager loads the last saved record with no other file present.
func TestRecoveryFromSavedState(t *testing.T) {
	m, root := newMgr(t)
	r := mustCreate(t, m)
	g := goodState()
	g.Counters.Crashes = 2
	last, err := m.Save(testGroup, g, r.Header.Rev)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := New(root, testWriter)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m2.Load(testGroup)
	if err != nil || !reflect.DeepEqual(got, last) {
		t.Fatalf("Load after restart: %+v %v", got, err)
	}
}

// A failed write is tried once more; two failures give a DiskError and keep the old record.
func TestDiskRetryThenError(t *testing.T) {
	m, root := newMgr(t)
	r := mustCreate(t, m)
	calls := 0
	real := m.writeFile
	m.writeFile = func(p string, b []byte) error {
		calls++
		if calls == 1 {
			return errors.New("disk hiccup")
		}
		return real(p, b)
	}
	r2, err := m.Save(testGroup, goodState(), r.Header.Rev)
	if err != nil || r2.Header.Rev != 2 || calls != 2 {
		t.Fatalf("retry: %v calls %d", err, calls)
	}
	before := readBytes(t, statePath(root, testGroup))
	m.writeFile = func(string, []byte) error { return errors.New("disk gone") }
	_, err = m.Save(testGroup, goodState(), 2)
	var de *DiskError
	if !errors.As(err, &de) || !strings.HasSuffix(de.Path, "state.json") {
		t.Fatalf("want DiskError, got %v", err)
	}
	if !bytes.Equal(before, readBytes(t, statePath(root, testGroup))) {
		t.Fatal("a failed write changed the record")
	}
}

// Remove checks the rev, deletes the file and reports a missing record.
func TestRemove(t *testing.T) {
	m, root := newMgr(t)
	r := mustCreate(t, m)
	if err := m.Remove(testGroup, 9); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("stale: %v", err)
	}
	if err := m.Remove(testGroup, r.Header.Rev); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(root, testGroup)); !os.IsNotExist(err) {
		t.Fatal("record kept")
	}
	if err := m.Remove(testGroup, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

// Save on a group with no record is a not-found error.
func TestSaveWithoutRecord(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.Save(testGroup, goodState(), 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// The unfinished-edits pointer round-trips and a group key with a path separator is refused.
func TestUnfinishedAndBadKey(t *testing.T) {
	m, _ := newMgr(t)
	g := goodState()
	g.Unfinished = &UnfinishedEdits{Task: "tsk-1", Files: []string{"a.go"}}
	r, err := m.Create(testGroup, g)
	if err != nil || !reflect.DeepEqual(r.State.Unfinished, g.Unfinished) {
		t.Fatalf("Unfinished: %v", err)
	}
	g.Unfinished = &UnfinishedEdits{Task: "tsk-1"}
	_, err = m.Save(testGroup, g, 1)
	wantInvalid(t, err, "state.unfinished.files")
	if _, err := m.Create("../escape", goodState()); err == nil {
		t.Fatal("a group key with a separator was accepted")
	}
	if _, err := m.Load("../escape"); err == nil {
		t.Fatal("Load accepted a separator")
	}
}
