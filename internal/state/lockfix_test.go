package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// A record lock is the operating system's, so a lock file with an old time still keeps a second writer out.
func TestMachineRecordLockIgnoresFileAge(t *testing.T) {
	m, home := newMach(t, "engine/1")
	if err := os.MkdirAll(filepath.Dir(machPath(home, KindUpdate)), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := machPath(home, KindUpdate) + ".lock"
	hold, err := core.FileLock(context.Background(), lock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.1", CheckedAt: t0}, 0)
	var de *DiskError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "holds the record lock") || time.Since(start) < machineLockWait {
		t.Fatalf("want a lock refusal after the wait, got %v", err)
	}
	if err := hold(); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.1", CheckedAt: t0}, 0); err != nil {
		t.Fatalf("after the holder let go: %v", err)
	}
}

// The per-process write mutex is taken only after the file lock, and a read never takes it.
func TestMachineMutexAfterFileLockAndReadsFree(t *testing.T) {
	m, home := newMach(t, "engine/1")
	if _, err := SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.1", CheckedAt: t0}, 0); err != nil {
		t.Fatal(err)
	}
	hold, err := core.FileLock(context.Background(), machPath(home, KindUpdate)+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := SaveMachine(m, KindUpdate, UpdateBody{Latest: "1.0.2", CheckedAt: t0}, 1)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if !m.kindMu[KindUpdate].TryLock() {
		t.Fatal("a writer waiting for the file lock already holds the process mutex")
	}
	m.kindMu[KindUpdate].Unlock()
	m.kindMu[KindUpdate].Lock()
	if _, err := LoadMachine[UpdateBody](m, KindUpdate); err != nil {
		t.Fatalf("a read blocked on the write mutex: %v", err)
	}
	m.kindMu[KindUpdate].Unlock()
	if err := hold(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("save after the holder let go: %v", err)
	}
}

// A first write that makes a new folder also syncs the folder above it, and later writes do not.
func TestNewFolderSyncsItsParent(t *testing.T) {
	var synced []string
	old := syncFolder
	syncFolder = func(dir string) error { synced = append(synced, dir); return nil }
	defer func() { syncFolder = old }()

	m, root := newMgr(t)
	mustCreate(t, m)
	if want := filepath.Join(root, "runs"); !slices.Contains(synced, want) {
		t.Fatalf("the runs folder was not synced: %v", synced)
	}
	synced = nil
	r, _ := m.Load(testGroup)
	if _, err := m.Save(testGroup, r.State, r.Header.Rev); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 0 {
		t.Fatalf("an existing folder was synced again: %v", synced)
	}

	synced = nil
	mm, home := newMach(t, "engine/1")
	if _, err := SaveMachine(mm, KindUpdate, UpdateBody{Latest: "1.0.1", CheckedAt: t0}, 0); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(synced, home) {
		t.Fatalf("the home folder was not synced for the new state folder: %v", synced)
	}
}

// Counters refuses what Load would set aside: unknown fields and schema failures.
func TestCountersUsesTheLoadChecks(t *testing.T) {
	m, root := newMgr(t)
	mustCreate(t, m)
	if c, err := m.Counters(testGroup); err != nil || c.Returns != 1 || c.Blocks == nil {
		t.Fatalf("a good record: %+v %v", c, err)
	}
	p := statePath(root, testGroup)
	good := string(readBytes(t, p))
	for name, bad := range map[string]string{
		"unknown field": strings.Replace(good, `"stage"`, `"bogus": 1, "stage"`, 1),
		"zero fence":    strings.Replace(good, `"fence": 3`, `"fence": 0`, 1),
	} {
		if bad == good {
			t.Fatalf("%s: the edit changed nothing", name)
		}
		writeRaw(t, p, bad)
		if c, err := m.Counters(testGroup); !errors.Is(err, ErrCorrupt) || c.Returns != 0 {
			t.Errorf("%s: want ErrCorrupt and no counters, got %+v %v", name, c, err)
		}
	}
}

// An open action with no kind names the kind field.
func TestEmptyActionKindNamesItsField(t *testing.T) {
	m, _ := newMgr(t)
	g := goodState()
	g.ActionsOpen = []OpenAction{{ID: "a1", Args: json.RawMessage(`{}`)}}
	_, err := m.Create(testGroup, g)
	wantInvalid(t, err, "state.actions_open[0].kind")
	g.ActionsOpen = []OpenAction{{Kind: "k", Args: json.RawMessage(`{}`)}}
	_, err = m.Create(testGroup, g)
	wantInvalid(t, err, "state.actions_open[0].id")
}

// A stored record with more than 64 request ids is corrupt; a save trims to 64.
func TestRequestsOverCapAreCorruptOnRead(t *testing.T) {
	m, root := newMgr(t)
	g := goodState()
	for i := 0; i < 70; i++ {
		g.Requests = append(g.Requests, fmt.Sprintf("req-%d", i))
	}
	r, err := m.Create(testGroup, g)
	if err != nil || len(r.State.Requests) != 64 || r.State.Requests[63] != "req-69" {
		t.Fatalf("save should keep the newest 64: %d %v", len(r.State.Requests), err)
	}
	p := statePath(root, testGroup)
	var w map[string]any
	if err := json.Unmarshal(readBytes(t, p), &w); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 70)
	for i := range ids {
		ids[i] = fmt.Sprintf("req-%d", i)
	}
	w["state"].(map[string]any)["requests"] = ids
	b, _ := json.Marshal(w)
	writeRaw(t, p, string(b))
	if _, err := m.Peek(testGroup); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Peek: want ErrCorrupt, got %v", err)
	}
	if _, err := m.Load(testGroup); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load: want ErrCorrupt, got %v", err)
	}
}

// A slot call decodes and encodes providers.json once, so it allocates well under the load-then-save path.
func TestSlotCallDoesLittleWork(t *testing.T) {
	m, _ := newMach(t, "engine/1")
	if _, err := m.TakeSlot("claude-cli", 1000); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_ = m.Record(fmt.Sprintf("link-%d", i), t0, "window")
	}
	got := testing.AllocsPerRun(20, func() {
		if _, err := m.TakeSlot("claude-cli", 1000); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("allocations per slot call: %.0f", got)
	if got > slotAllocBudget {
		t.Fatalf("a slot call allocated %.0f times, budget %d", got, slotAllocBudget)
	}
}

// slotAllocBudget is the most allocations a slot call may make; the load-then-save path made about 3,400.
const slotAllocBudget = 1500
