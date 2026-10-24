package state

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A v0 fixture reads as the current version for a group record and a machine record, and the read writes nothing.
func TestVersionZeroReadsAsCurrent(t *testing.T) {
	m, root := newMgr(t)
	p := statePath(root, testGroup)
	body := `{"v":0,"group":"` + testGroup + `","rev":3,"ts":"2026-10-14T16:01:40Z","writer":"engine/r/1",
	 "state":{"run":"r","group":"` + testGroup + `","stage":"build","fence":1,"config_hash":"` + testHash + `"}}`
	writeRaw(t, p, body)
	r, err := m.Load(testGroup)
	if err != nil || r.Header.V != Version || r.Header.Rev != 3 {
		t.Fatalf("Load: %+v %v", r.Header, err)
	}
	if string(readBytes(t, p)) != body {
		t.Fatal("the read rewrote the record")
	}
	next, err := m.Save(testGroup, r.State, r.Header.Rev)
	if err != nil || next.Header.V != Version || !strings.Contains(string(readBytes(t, p)), `"v": 5`) {
		t.Fatalf("Save after migration: %+v %v", next.Header, err)
	}

	mm, home := newMach(t, "engine/1")
	writeRaw(
		t,
		machPath(home, KindUpdate),
		`{"v":0,"kind":"update","rev":1,"ts":"2026-10-14T16:01:40Z","writer":"cli/1","body":{"latest":"1.0.0",`+
			`"checked_at":"2026-10-14T16:01:40Z","previous":""}}`,
	)
	u, err := LoadMachine[UpdateBody](mm, KindUpdate)
	if err != nil || u.Header.V != Version || u.Body.Latest != "1.0.0" {
		t.Fatalf("LoadMachine: %+v %v", u, err)
	}
}

// A newer version is refused and the file stays where it is.
func TestNewerVersionUntouched(t *testing.T) {
	m, root := newMgr(t)
	p := statePath(root, testGroup)
	writeRaw(t, p, `{"v":6,"anything":true}`)
	if _, err := m.Load(testGroup); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("Load: %v", err)
	}
	if _, err := m.Peek(testGroup); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("Peek: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a newer record was moved")
	}
}

// A step chain runs from the stored version to the current one, and a gap is a failed read.
func TestMigrateChain(t *testing.T) {
	reg := map[migrationKey]Migration{
		{
			"",
			1,
		}: {From: 1, Step: func(b []byte) ([]byte, error) { return []byte(strings.Replace(string(b), "a", "b", 1)), nil }},
		{
			"",
			2,
		}: {From: 2, Step: func(b []byte) ([]byte, error) { return []byte(strings.Replace(string(b), "b", "c", 1)), nil }},
	}
	out, v, err := runMigrations(reg, "", 3, 1, []byte(`{"v":1,"x":"a"}`))
	if err != nil || v != 3 || !strings.Contains(string(out), `"v":3`) || !strings.Contains(string(out), `"x":"c"`) {
		t.Fatalf("chain: %s %d %v", out, v, err)
	}
	if _, _, err := runMigrations(reg, "", 4, 3, []byte(`{"v":3}`)); err == nil {
		t.Fatal("a missing step was skipped")
	}
	if _, _, err := runMigrations(reg, "", 3, -1, []byte(`{"v":-1}`)); err == nil {
		t.Fatal("a negative version migrated")
	}
}

// A group saved before the retry budget loads with the budget at the limit and none of the old counters.
func TestMigrationSwapsTheCountingUpLimitsForTheBudget(t *testing.T) {
	m, root := newMgr(t)
	p := statePath(root, testGroup)
	writeRaw(t, p, `{"v":1,"group":"`+testGroup+`","rev":4,"ts":"2026-10-14T16:01:40Z","writer":"engine/r/1",
	 "state":{"run":"r","group":"`+testGroup+`","stage":"blocked","prior":"test_review","fence":1,
	 "config_hash":"`+testHash+`",
	 "block":{"reason":"returns"},
	 "counters":{"returns":3,"attempts":5,"retries":2,"crashes":1,"blocks":{"returns":2,"attempts":1,"flap":1},
	  "wall_used_s":60,"wall_extended":false,"tokens_used":10},
	 "test_review":{"tester":{"result":"","restarts":1},"reviewer":{"result":"","restarts":0},
	  "findings":[],"not_met":[],"failed":[]},
	 "tasks":[],"actions_open":[],"resume":[],"owed":[],"requests":[],"scope_added":[]}}`)

	r, err := m.Load(testGroup)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	c := r.State.Counters
	if c.RetriesLeft != 3 || c.Overrides != 0 || c.Crashes != 1 || c.WallUsedS != 60 || c.TokensUsed != 10 {
		t.Fatalf("counters after migration: %+v", c)
	}
	if c.Blocks["retries"] != 3 || c.Blocks["flap"] != 1 || len(c.Blocks) != 2 {
		t.Fatalf("blocks after migration: %v", c.Blocks)
	}
	if r.State.Block.Reason != ReasonRetries {
		t.Fatalf("a block on a spent counter must become retries, got %q", r.State.Block.Reason)
	}

	next, err := m.Save(testGroup, r.State, r.Header.Rev)
	if err != nil {
		t.Fatalf("Save after migration: %v", err)
	}

	raw := string(readBytes(t, p))
	for _, gone := range []string{`"returns"`, `"attempts"`, `"restarts"`} {
		if strings.Contains(raw, gone) {
			t.Errorf("the saved record still holds %s", gone)
		}
	}
	if next.Header.V != Version {
		t.Fatalf("saved version %d", next.Header.V)
	}
}

// A version 1 group that never blocked on a counter keeps its block reason and an unmapped stage.
func TestMigrationLeavesOtherBlocksAlone(t *testing.T) {
	m, root := newMgr(t)
	writeRaw(t, statePath(root, testGroup), `{"v":1,"group":"`+testGroup+`","rev":1,"ts":"2026-10-14T16:01:40Z",
	 "writer":"engine/r/1",
	 "state":{"run":"r","group":"`+testGroup+`","stage":"blocked","prior":"build","fence":1,"config_hash":"`+testHash+`",
	 "block":{"reason":"flap"},"counters":{"blocks":{"flap":1}}}}`)

	r, err := m.Load(testGroup)
	if err != nil || r.State.Block.Reason != ReasonFlap || r.State.Counters.RetriesLeft != 3 {
		t.Fatalf("Load: %+v %v", r.State.Counters, err)
	}
}
