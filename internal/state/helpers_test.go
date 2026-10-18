package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	testGroup  = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f30"
	testRun    = "0198f3a2-6c1e-7b44-9d0a-3f5e1c2b7a10"
	testWriter = "engine/" + testRun + "/48213"
	testHash   = "023c648e364af6cf885bc15be12621607595a7ef64533179971d9e8d0603cb2c"
)

// newMgr opens a group manager over a temporary folder with the engine as writer.
func newMgr(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	m, err := New(root, testWriter)
	if err != nil {
		t.Fatal(err)
	}
	return m, root
}

// statePath returns where the manager keeps a group's record.
func statePath(root, group string) string {
	return filepath.Join(root, "runs", group, "state.json")
}

// mustCreate creates the test group's record and returns it.
func mustCreate(t *testing.T, m *Manager) Record {
	t.Helper()
	r, err := m.Create(testGroup, goodState())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return r
}

// goodState returns a valid group state for the test group, with no nil slice or map.
func goodState() GroupState {
	return GroupState{
		Run:        testRun,
		Group:      testGroup,
		Stage:      StageBuild,
		Epoch:      4,
		Fence:      3,
		ConfigHash: testHash,
		Counters:   Counters{Returns: 1, Attempts: 1, Blocks: map[string]int{}, WallUsedS: 2460},
		Tasks: []TaskState{
			{ID: "tsk-add-lock", State: "claimed", Session: "s1"},
		},
		TestReview:  TestReview{Findings: []Finding{}, NotMet: []string{}, Failed: []string{}},
		ActionsOpen: []OpenAction{},
		Resume:      []ResumeRef{{Session: "s1", Handle: "s1", Link: "claude-cli"}},
		Owed:        []string{},
		Requests:    []string{},
		ScopeAdded:  []string{},
		LastEventAt: time.Date(2026, 10, 14, 16, 1, 40, 0, time.UTC),
	}
}

// readBytes returns a file's content or fails the test.
func readBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantInvalid fails unless err is an InvalidError naming field.
func wantInvalid(t *testing.T, err error, field string) {
	t.Helper()
	var ie *InvalidError
	if !errors.As(err, &ie) || ie.Field != field {
		t.Fatalf("want InvalidError on %q, got %v", field, err)
	}
}

// writeRaw writes a file, making its folder first.
func writeRaw(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
