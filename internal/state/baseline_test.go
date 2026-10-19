package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const otherGroup = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f31"

// TestBaselineSurvivesARestart writes a group's baseline, opens a new manager and reads it back.
func TestBaselineSurvivesARestart(t *testing.T) {
	m, root := newMgr(t)
	want := BaselineRecord{Tree: testHash, Passed: map[string]bool{"pkg.TestA": true, "pkg.TestB": false}}
	if err := m.SaveBaseline(testGroup, want); err != nil {
		t.Fatal(err)
	}

	again, err := New(root, testWriter)
	if err != nil {
		t.Fatal(err)
	}

	got, err := again.LoadBaseline(testGroup)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tree != want.Tree || !got.Passed["pkg.TestA"] || got.Passed["pkg.TestB"] || len(got.Passed) != 2 {
		t.Fatalf("baseline = %+v", got)
	}
}

// TestBaselineOfOneGroupServesAnotherOnTheSameTree checks that one entry serves every group cut from the same base.
func TestBaselineOfOneGroupServesAnotherOnTheSameTree(t *testing.T) {
	m, _ := newMgr(t)
	if err := m.SaveBaseline(testGroup, BaselineRecord{Tree: testHash, Passed: map[string]bool{"a.T": true}}); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveBaseline(otherGroup, BaselineRecord{Tree: "other", Passed: map[string]bool{"b.T": true}}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := m.FindBaseline(testHash)
	if err != nil || !ok || !got.Passed["a.T"] {
		t.Fatalf("find = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := m.FindBaseline("unknown"); ok {
		t.Fatal("a tree nobody ran has a baseline")
	}
}

// TestBaselineWriteNeedsTheEngine checks that only the engine writes, as it does for a group record.
func TestBaselineWriteNeedsTheEngine(t *testing.T) {
	m, err := New(t.TempDir(), "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveBaseline(testGroup, BaselineRecord{Tree: testHash, Passed: map[string]bool{}}); !errors.Is(err, ErrNotWriter) {
		t.Fatalf("err = %v", err)
	}
}

// TestBaselineDamagedFileIsSetAside checks that a damaged file reads as corrupt and is renamed, never repaired.
func TestBaselineDamagedFileIsSetAside(t *testing.T) {
	m, root := newMgr(t)
	p := filepath.Join(root, "runs", testGroup, "baseline.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.LoadBaseline(testGroup); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("damaged file stayed: %v", err)
	}
	if _, err := m.LoadBaseline(testGroup); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after set-aside err = %v", err)
	}
}

// TestBaselineRefusesAnEmptyTree checks that an entry without a tree hash has no key and is refused.
func TestBaselineRefusesAnEmptyTree(t *testing.T) {
	m, _ := newMgr(t)
	var inv *InvalidError
	if err := m.SaveBaseline(testGroup, BaselineRecord{Passed: map[string]bool{}}); !errors.As(err, &inv) {
		t.Fatalf("err = %v", err)
	}
}
