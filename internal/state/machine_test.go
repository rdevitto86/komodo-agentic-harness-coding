package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// newMach opens a machine manager over a temporary home folder.
func newMach(t *testing.T, writer string) (*Machine, string) {
	t.Helper()
	home := t.TempDir()
	m, err := NewMachine(home, writer)
	if err != nil {
		t.Fatal(err)
	}
	return m, home
}

// machPath returns where a machine-wide record lives.
func machPath(home string, k MachineKind) string {
	return filepath.Join(home, "state", string(k)+".json")
}

var t0 = time.Date(2026, 10, 14, 16, 1, 40, 0, time.UTC)

const hash64 = "1a9509c4c1d09512211b773b90b562dc6e5a2fbbb344ccd72c2a230a51a86353"

// sampleSchedule returns a valid queue with one entry.
func sampleSchedule() ScheduleBody {
	return ScheduleBody{Entries: []ScheduleEntry{{
		Target: "ep-1", Root: "/Users/dev/src/shop", RunID: "0192e5a0-2a10-7c52-9e0b-3d4f8a1b6c72",
		At: t0.Add(17 * time.Hour), QueuedAt: t0,
		Start: bus.RunStart{Target: "ep-1", Groups: []string{"tg-1.1", "tg-1.2"}, Source: "cli", Schedule: t0.Add(17 * time.Hour),
			Repo: "github.com/acme/shop", Root: "/Users/dev/src/shop", CommonDir: "/Users/dev/src/shop/.git"},
	}}}
}

// roundTrip saves a body at rev 0, loads it and returns both records.
func roundTrip[T any](t *testing.T, m *Machine, k MachineKind, body T) (MachineRecord[T], MachineRecord[T]) {
	t.Helper()
	saved, err := SaveMachine(m, k, body, 0)
	if err != nil {
		t.Fatalf("SaveMachine %s: %v", k, err)
	}
	got, err := LoadMachine[T](m, k)
	if err != nil {
		t.Fatalf("LoadMachine %s: %v", k, err)
	}
	return saved, got
}

// Each of the four records round-trips through SaveMachine and LoadMachine and comes back equal.
func TestMachineRecordsRoundTrip(t *testing.T) {
	m, home := newMach(t, "engine/48301")
	probe := json.RawMessage(`{"OK":true,"Path":"/usr/local/bin/claude","Ctx":1000000}`)
	providers := ProvidersBody{Links: map[string]LinkRecord{
		"claude-cli": {Probe: probe, ResetAt: t0.Add(5 * time.Hour), Cap: "window", Sessions: 2,
			Windows: map[string]WindowRecord{"five_hour": {PercentUsed: 83.2, ResetAt: t0.Add(5 * time.Hour), Sessions: 14}}},
		"anthropic-api": {Probe: json.RawMessage(`{"OK":false,"Reason":"no key"}`)},
	}}
	update := UpdateBody{Latest: "1.0.1", CheckedAt: t0, Previous: "1.0.0"}
	install := InstallBody{
		Entries:    []InstallEntry{{Target: "/Users/dev/.komodo/bin/komodo", Hash: hash64, Step: 1}, {Target: "/Users/dev/.claude/settings.json", Entry: "hooks.PreToolUse", Hash: hash64, Step: 4}},
		Selections: InstallSelections{Hosts: []string{"claude"}},
	}
	schedule := sampleSchedule()

	check := func(name string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: saved %+v, loaded %+v", name, a, b)
		}
	}
	s1, g1 := roundTrip(t, m, KindProviders, providers)
	check("providers", s1, g1)
	check("providers body", g1.Body, providers)
	s2, g2 := roundTrip(t, m, KindUpdate, update)
	check("update", s2, g2)
	s3, g3 := roundTrip(t, m, KindInstall, install)
	check("install", s3, g3)
	s4, g4 := roundTrip(t, m, KindSchedule, schedule)
	check("schedule", s4, g4)
	check("schedule body", g4.Body, schedule)
	h := g4.Header
	if h.V != 1 || h.Kind != KindSchedule || h.Rev != 1 || h.Writer != "engine/48301" || h.TS.Location() != time.UTC {
		t.Errorf("header: %+v", h)
	}
	for _, k := range []MachineKind{KindProviders, KindUpdate, KindInstall, KindSchedule} {
		if _, err := os.Stat(machPath(home, k)); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
}

// A first run gets ErrNotFound and an empty body that is never null.
func TestLoadMachineFirstRun(t *testing.T) {
	m, _ := newMach(t, "engine/1")
	r, err := LoadMachine[ScheduleBody](m, KindSchedule)
	if !errors.Is(err, ErrNotFound) || r.Body.Entries == nil || r.Header.Rev != 0 {
		t.Fatalf("schedule: %+v %v", r, err)
	}
	p, err := LoadMachine[ProvidersBody](m, KindProviders)
	if !errors.Is(err, ErrNotFound) || p.Body.Links == nil {
		t.Fatalf("providers: %+v %v", p, err)
	}
}

// A write that breaks a rule is refused with the field path and the file stays byte-identical.
func TestSaveMachineRefusals(t *testing.T) {
	m, home := newMach(t, "engine/1")
	good := sampleSchedule()
	r, err := SaveMachine(m, KindSchedule, good, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, machPath(home, KindSchedule))

	nilEntries := ScheduleBody{}
	_, err = SaveMachine(m, KindSchedule, nilEntries, r.Header.Rev)
	wantInvalid(t, err, "body.entries")

	empty := sampleSchedule()
	empty.Entries[0].Target = ""
	_, err = SaveMachine(m, KindSchedule, empty, r.Header.Rev)
	wantInvalid(t, err, "body.entries[0].target")

	noGroups := sampleSchedule()
	noGroups.Entries[0].Start.Groups = []string{}
	_, err = SaveMachine(m, KindSchedule, noGroups, r.Header.Rev)
	wantInvalid(t, err, "body.entries[0].start.groups")

	noAt := sampleSchedule()
	noAt.Entries[0].At = time.Time{}
	_, err = SaveMachine(m, KindSchedule, noAt, r.Header.Rev)
	wantInvalid(t, err, "body.entries[0].at")

	if !bytes.Equal(before, readBytes(t, machPath(home, KindSchedule))) {
		t.Fatal("a refused write changed the file")
	}

	_, err = SaveMachine(m, KindProviders, ProvidersBody{Links: map[string]LinkRecord{"a": {Sessions: -1}}}, 0)
	wantInvalid(t, err, "body.links.a.sessions")
	_, err = SaveMachine(m, KindProviders, ProvidersBody{Links: map[string]LinkRecord{"a": {Cap: "huge"}}}, 0)
	wantInvalid(t, err, "body.links.a.cap")
	_, err = SaveMachine(m, KindProviders, ProvidersBody{Links: map[string]LinkRecord{"a": {Probe: json.RawMessage(`[1]`)}}}, 0)
	wantInvalid(t, err, "body.links.a.probe")
	_, err = SaveMachine(m, KindProviders, ProvidersBody{}, 0)
	wantInvalid(t, err, "body.links")
	_, err = SaveMachine(m, KindInstall, InstallBody{Entries: []InstallEntry{{Target: "/x", Hash: "short", Step: 1}}}, 0)
	wantInvalid(t, err, "body.entries[0].hash")
	_, err = SaveMachine(m, KindInstall, InstallBody{Entries: []InstallEntry{{Target: "/x", Hash: hash64}}}, 0)
	wantInvalid(t, err, "body.entries[0].step")
	_, err = SaveMachine(m, KindUpdate, UpdateBody{Latest: "one", CheckedAt: t0}, 0)
	wantInvalid(t, err, "body.latest")
	_, err = SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.0"}, 0)
	wantInvalid(t, err, "body.checked_at")
	_, err = SaveMachine(m, MachineKind("other"), UpdateBody{}, 0)
	wantInvalid(t, err, "kind")
	_, err = SaveMachine(m, KindUpdate, ScheduleBody{Entries: []ScheduleEntry{}}, 0)
	wantInvalid(t, err, "body")
}

// A component outside the four is refused with ErrNotWriter.
func TestSaveMachineNotWriter(t *testing.T) {
	m, home := newMach(t, "intake/1")
	_, err := SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.0", CheckedAt: t0}, 0)
	if !errors.Is(err, ErrNotWriter) {
		t.Fatalf("want ErrNotWriter, got %v", err)
	}
	if _, err := os.Stat(machPath(home, KindUpdate)); !os.IsNotExist(err) {
		t.Fatal("a refused write created a file")
	}
	for _, c := range []string{"resource", "model", "cli", "engine"} {
		w, err := NewMachine(t.TempDir(), c)
		if err != nil {
			t.Fatal(err)
		}
		if r, err := SaveMachine(w, KindUpdate, UpdateBody{Latest: "1.0.0", CheckedAt: t0}, 0); err != nil || !strings.HasPrefix(r.Header.Writer, c+"/") {
			t.Fatalf("%s: %+v %v", c, r.Header, err)
		}
	}
}

// Rev 0 creates only once; a stale rev is refused and the record is unchanged.
func TestSaveMachineRev(t *testing.T) {
	m, home := newMach(t, "cli/1")
	body := UpdateBody{Latest: "1.0.0", CheckedAt: t0}
	r, err := SaveMachine(m, KindUpdate, body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMachine(m, KindUpdate, body, 0); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v", err)
	}
	before := readBytes(t, machPath(home, KindUpdate))
	if _, err := SaveMachine(m, KindUpdate, body, 7); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("stale: %v", err)
	}
	if !bytes.Equal(before, readBytes(t, machPath(home, KindUpdate))) {
		t.Fatal("a stale write changed the record")
	}
	r2, err := SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.1", CheckedAt: t0}, r.Header.Rev)
	if err != nil || r2.Header.Rev != 2 {
		t.Fatalf("Save: %+v %v", r2.Header, err)
	}
	if _, err := SaveMachine(m, KindInstall, InstallBody{Entries: []InstallEntry{}}, 3); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("rev on a missing record: %v", err)
	}
}

// A second process (the CLI) that read an old rev is refused after the engine wrote.
func TestMachineTwoWritersStale(t *testing.T) {
	eng, home := newMach(t, "engine/1")
	cli, err := NewMachine(home, "cli/2")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := SaveMachine(eng, KindUpdate, UpdateBody{Latest: "1.0.0", CheckedAt: t0}, 0)
	read, err := LoadMachine[UpdateBody](cli, KindUpdate)
	if err != nil || read.Header.Rev != r.Header.Rev {
		t.Fatal(err)
	}
	if _, err := SaveMachine(eng, KindUpdate, UpdateBody{Latest: "1.0.1", CheckedAt: t0}, r.Header.Rev); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMachine(cli, KindUpdate, UpdateBody{Latest: "1.0.2", CheckedAt: t0}, read.Header.Rev); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("want ErrStaleRev, got %v", err)
	}
	again, _ := LoadMachine[UpdateBody](cli, KindUpdate)
	if _, err := SaveMachine(cli, KindUpdate, UpdateBody{Latest: "1.0.2", CheckedAt: t0}, again.Header.Rev); err != nil {
		t.Fatalf("retry after reread: %v", err)
	}
}

// A damaged record is renamed by a writer's LoadMachine and returns ErrCorrupt; a bad shape is damage too.
func TestLoadMachineCorrupt(t *testing.T) {
	m, home := newMach(t, "engine/1")
	p := machPath(home, KindSchedule)
	writeRaw(t, p, `{"v":1,"kind":"schedule","rev":`)
	if _, err := LoadMachine[ScheduleBody](m, KindSchedule); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("damaged record kept")
	}
	bad, _ := filepath.Glob(p + ".bad-*")
	if len(bad) != 1 {
		t.Fatalf("quarantine files: %v", bad)
	}
	if _, err := LoadMachine[ScheduleBody](m, KindSchedule); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after quarantine: %v", err)
	}
	// A kind that disagrees with the file name, and an unknown field, are damage.
	writeRaw(t, p, `{"v":1,"kind":"update","rev":1,"ts":"2026-10-14T16:01:40Z","writer":"engine/1","body":{"entries":[]}}`)
	if _, err := LoadMachine[ScheduleBody](m, KindSchedule); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("kind mismatch: %v", err)
	}
	writeRaw(t, p, `{"v":1,"kind":"schedule","rev":1,"ts":"2026-10-14T16:01:40Z","writer":"engine/1","body":{"entries":[],"extra":1}}`)
	if _, err := LoadMachine[ScheduleBody](m, KindSchedule); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unknown field: %v", err)
	}
	// A reader that is not a writer leaves the file.
	other, _ := NewMachine(home, "intake/9")
	writeRaw(t, p, `garbage`)
	if _, err := LoadMachine[ScheduleBody](other, KindSchedule); !errors.Is(err, ErrCorrupt) {
		t.Fatal("non-writer Load")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a non-writer moved the record")
	}
}

// An absent list in the file reads back as []; a newer version is left in place.
func TestLoadMachineAbsentAndNewer(t *testing.T) {
	m, home := newMach(t, "engine/1")
	p := machPath(home, KindSchedule)
	writeRaw(t, p, `{"v":1,"kind":"schedule","rev":1,"ts":"2026-10-14T16:01:40Z","writer":"engine/1","body":{}}`)
	r, err := LoadMachine[ScheduleBody](m, KindSchedule)
	if err != nil || r.Body.Entries == nil {
		t.Fatalf("absent entries: %+v %v", r, err)
	}
	writeRaw(t, p, `{"v":9,"kind":"schedule","body":{"future":true}}`)
	if _, err := LoadMachine[ScheduleBody](m, KindSchedule); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("want ErrNewerVersion, got %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a newer record was moved")
	}
}

// An entry whose time has passed, found after a restart, is returned and removed by a save naming the rev read.
func TestScheduleEntryFiresAfterRestart(t *testing.T) {
	m, home := newMach(t, "engine/1")
	body := sampleSchedule()
	body.Entries[0].At = time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := SaveMachine(m, KindSchedule, body, 0); err != nil {
		t.Fatal(err)
	}
	m2, _ := NewMachine(home, "engine/2")
	r, err := LoadMachine[ScheduleBody](m2, KindSchedule)
	if err != nil || len(r.Body.Entries) != 1 || !r.Body.Entries[0].At.Before(time.Now()) {
		t.Fatalf("Load: %+v %v", r, err)
	}
	r.Body.Entries = []ScheduleEntry{}
	left, err := SaveMachine(m2, KindSchedule, r.Body, r.Header.Rev)
	if err != nil || len(left.Body.Entries) != 0 {
		t.Fatalf("Save: %+v %v", left, err)
	}
}

// Times written in another zone are stored in UTC.
func TestMachineTimesStoredUTC(t *testing.T) {
	m, home := newMach(t, "cli/1")
	zone := time.FixedZone("x", 3*3600)
	_, err := SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.0", CheckedAt: t0.In(zone)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readBytes(t, machPath(home, KindUpdate))), `"checked_at": "2026-10-14T16:01:40Z"`) {
		t.Fatalf("not UTC: %s", readBytes(t, machPath(home, KindUpdate)))
	}
}

// Session counts reset to 0 when the engine starts; other fields stay.
func TestResetSessions(t *testing.T) {
	m, _ := newMach(t, "engine/1")
	if err := m.ResetSessions(); err != nil {
		t.Fatalf("no file: %v", err)
	}
	body := ProvidersBody{Links: map[string]LinkRecord{
		"a": {Probe: json.RawMessage(`{}`), Sessions: 3, Cap: "window", ResetAt: t0},
		"b": {Probe: json.RawMessage(`{}`), Sessions: 1},
	}}
	if _, err := SaveMachine(m, KindProviders, body, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetSessions(); err != nil {
		t.Fatal(err)
	}
	r, _ := LoadMachine[ProvidersBody](m, KindProviders)
	if r.Body.Links["a"].Sessions != 0 || r.Body.Links["b"].Sessions != 0 || r.Body.Links["a"].Cap != "window" || r.Header.Rev != 2 {
		t.Fatalf("after reset: %+v rev %d", r.Body, r.Header.Rev)
	}
	if err := m.ResetSessions(); err != nil {
		t.Fatal(err)
	}
	if r, _ := LoadMachine[ProvidersBody](m, KindProviders); r.Header.Rev != 2 {
		t.Fatal("a reset with nothing to reset wrote the file")
	}
}

// TakeSlot counts up to the most, ReleaseSlot counts down, and Record keeps the reset time.
func TestLimitSlotsAndRecord(t *testing.T) {
	m, _ := newMach(t, "engine/1")
	for i := 0; i < 2; i++ {
		if ok, err := m.TakeSlot("claude-cli", 2); !ok || err != nil {
			t.Fatalf("take %d: %v %v", i, ok, err)
		}
	}
	if ok, err := m.TakeSlot("claude-cli", 2); ok || err != nil {
		t.Fatalf("third take: %v %v", ok, err)
	}
	if ok, _ := m.TakeSlot("local", 1); !ok {
		t.Fatal("a second link has its own count")
	}
	if err := m.ReleaseSlot("claude-cli"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.TakeSlot("claude-cli", 2); !ok {
		t.Fatal("a released slot was not free")
	}
	for i := 0; i < 5; i++ {
		_ = m.ReleaseSlot("claude-cli")
	}
	r, _ := LoadMachine[ProvidersBody](m, KindProviders)
	if r.Body.Links["claude-cli"].Sessions != 0 {
		t.Fatalf("a count fell below 0: %+v", r.Body.Links["claude-cli"])
	}
	if _, limited := m.ResetAt("claude-cli"); limited {
		t.Fatal("limited with no record")
	}
	if err := m.Record("claude-cli", t0.Add(time.Hour), "window"); err != nil {
		t.Fatal(err)
	}
	if at, limited := m.ResetAt("claude-cli"); !limited || !at.Equal(t0.Add(time.Hour)) {
		t.Fatalf("ResetAt: %v %v", at, limited)
	}
	if err := m.Record("claude-cli", t0, "huge"); err == nil {
		t.Fatal("a bad cap was stored")
	}
}

// Two machine managers sharing one file lose no slot to a stale rev.
func TestSlotsRetryOnStaleRev(t *testing.T) {
	a, home := newMach(t, "engine/1")
	b, _ := NewMachine(home, "model/2")
	var wg sync.WaitGroup
	for _, m := range []*Machine{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if ok, err := m.TakeSlot("l", 100); !ok || err != nil {
					t.Error(ok, err)
				}
			}
		}()
	}
	wg.Wait()
	r, _ := LoadMachine[ProvidersBody](a, KindProviders)
	if r.Body.Links["l"].Sessions != 20 {
		t.Fatalf("sessions: %d", r.Body.Links["l"].Sessions)
	}
}

// Windows are saved under their link and read back; the rest of a link's record stays.
func TestWindows(t *testing.T) {
	m, _ := newMach(t, "resource/5")
	ctx := context.Background()
	if w, err := m.LoadWindows(ctx); err != nil || len(w) != 0 {
		t.Fatalf("first run: %v %v", w, err)
	}
	if err := m.Record("claude-cli", t0, "window"); err != nil {
		t.Fatal(err)
	}
	k := WindowKey{Link: "claude-cli", Window: "five_hour"}
	rec := WindowRecord{PercentUsed: 83.2, ResetAt: t0.Add(time.Hour), Sessions: 14}
	if err := m.SaveWindow(ctx, k, rec); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveWindow(ctx, WindowKey{Link: "new-link", Window: "weekly"}, rec); err != nil {
		t.Fatal(err)
	}
	w, err := m.LoadWindows(ctx)
	if err != nil || len(w) != 2 || w[k] != rec {
		t.Fatalf("LoadWindows: %v %v", w, err)
	}
	if at, ok := m.ResetAt("claude-cli"); !ok || !at.Equal(t0) {
		t.Fatalf("the link's reset time was lost: %v", at)
	}
	if err := m.SaveWindow(ctx, k, WindowRecord{PercentUsed: -1}); err == nil {
		t.Fatal("a negative percent was stored")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.SaveWindow(cancelled, k, rec); err == nil {
		t.Fatal("SaveWindow ignored a cancelled context")
	}
}
