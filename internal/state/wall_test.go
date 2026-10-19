package state

import "testing"

// v2Record returns a version 2 group record with the given block and counters.
func v2Record(block, counters string) string {
	return `{"v":2,"group":"` + testGroup + `","rev":2,"ts":"2026-10-14T16:01:40Z","writer":"engine/r/1",
	 "state":{"run":"r","group":"` + testGroup + `","stage":"blocked","prior":"build","fence":1,
	 "config_hash":"` + testHash + `","block":` + block + `,"counters":` + counters + `}}`
}

// A group saved blocked on wall resumes on a plain run with a fresh try: its base rises to the used time.
func TestMigrationGivesAWallBlockedGroupFreshTryTime(t *testing.T) {
	m, root := newMgr(t)
	writeRaw(t, statePath(root, testGroup), v2Record(`{"reason":"wall"}`,
		`{"retries_left":2,"overrides":0,"crashes":0,"blocks":{"wall":1},"wall_used_s":7200,"wall_extended":true,"tokens_used":5}`))

	r, err := m.Load(testGroup)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	c := r.State.Counters
	if c.WallBaseS != 7200 || c.WallUsedS != 7200 || c.WallExtTry != 0 || c.Tries != 1 || c.RetriesLeft != 2 {
		t.Fatalf("counters after migration: %+v", c)
	}
	if _, err := m.Save(testGroup, r.State, r.Header.Rev); err != nil {
		t.Fatalf("Save after migration: %v", err)
	}
}

// An extension granted before the change counts as the first try's extension; the base stays at 0.
func TestMigrationKeepsAGrantedExtensionOnTheFirstTry(t *testing.T) {
	m, root := newMgr(t)
	writeRaw(t, statePath(root, testGroup), v2Record(`{"reason":"flap"}`,
		`{"retries_left":1,"overrides":1,"crashes":0,"blocks":{"flap":1},"wall_used_s":4000,"wall_extended":true,"tokens_used":0}`))

	r, err := m.Load(testGroup)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c := r.State.Counters; c.WallExtTry != 1 || c.Tries != 1 || c.WallBaseS != 0 || c.Overrides != 1 {
		t.Fatalf("counters after migration: %+v", c)
	}
}

// A write that lowers the try, the base or the extension try is refused, and a base over the used time is invalid.
func TestTheTryCountersOnlyRise(t *testing.T) {
	for name, edit := range map[string]func(c *Counters){
		"tries":        func(c *Counters) { c.Tries = 1 },
		"wall_base_s":  func(c *Counters) { c.WallBaseS = 10 },
		"wall_ext_try": func(c *Counters) { c.WallExtTry = 1 },
	} {
		m, _ := newMgr(t)
		g := goodState()
		g.Counters = Counters{Tries: 3, WallUsedS: 9000, WallBaseS: 100, WallExtTry: 2, Blocks: map[string]int{}}
		rec, err := m.Create(testGroup, g)
		if err != nil {
			t.Fatalf("%s: Create: %v", name, err)
		}

		low := rec.State
		edit(&low.Counters)
		if _, err := m.Save(testGroup, low, rec.Header.Rev); err == nil {
			t.Errorf("%s: a lowered counter was saved", name)
		}
	}

	m, _ := newMgr(t)
	g := goodState()
	g.Counters = Counters{Tries: 1, WallUsedS: 10, WallBaseS: 11, Blocks: map[string]int{}}
	if _, err := m.Create(testGroup, g); err == nil {
		t.Error("a base above the used time was saved")
	}
}
