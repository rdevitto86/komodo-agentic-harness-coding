package state

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"
)

// Migration upgrades the record bytes of one version to the next, in memory; Kind is empty for a group record.
type Migration struct {
	From int
	Kind MachineKind
	Step func(old []byte) ([]byte, error)
}

// migrationKey identifies a step by the record family and the version it upgrades from.
type migrationKey struct {
	scope string
	from  int
}

// Version is the schema version this binary writes.
const Version = 4

// migratedRetries is the retries a group saved before the retry budget starts with: the shipped limits.group_retries.
const migratedRetries = 3

var (
	migrationMu sync.RWMutex
	migrations  = map[migrationKey]Migration{}
)

// RegisterMigration adds a step; called from init in this file.
func RegisterMigration(m Migration) {
	migrationMu.Lock()
	defer migrationMu.Unlock()
	migrations[migrationKey{string(m.Kind), m.From}] = m
}

// Version 0 has the same shape as version 1, and a machine record has the same shape in versions 2 to 4, so those steps
// change nothing; a group record from version 1 swaps its three counting-up limits for the retry budget, one from
// version 2 moves its wall time to a per-try base, and one from version 3 renames the tester to qa.
func init() {
	identity := func(old []byte) ([]byte, error) { return old, nil }
	RegisterMigration(Migration{From: 0, Step: identity})
	RegisterMigration(Migration{From: 1, Step: retryBudget})
	RegisterMigration(Migration{From: 2, Step: wallPerTry})
	RegisterMigration(Migration{From: 3, Step: qaRename})
	for _, k := range machineKinds {
		RegisterMigration(Migration{From: 0, Kind: k, Step: identity})
		RegisterMigration(Migration{From: 1, Kind: k, Step: identity})
		RegisterMigration(Migration{From: 2, Kind: k, Step: identity})
		RegisterMigration(Migration{From: 3, Kind: k, Step: identity})
	}
}

// retryBudget upgrades a version 1 group record: the returns, attempts and retries counters and tester and reviewer
// restarts are dropped, retries_left starts at the limit, and a block on a spent counter becomes a block on
// retries.
func retryBudget(old []byte) ([]byte, error) {
	rec, ok := objectOf(old)
	if !ok {
		return old, nil
	}

	st, ok := objectOf(rec["state"])
	if !ok {
		return old, nil
	}
	if c, ok := objectOf(st["counters"]); ok {
		migrateCounters(c)
		st["counters"] = encodeObject(c)
	}
	if b, ok := objectOf(st["block"]); ok {
		if r := string(b["reason"]); slices.Contains(spentReasons, r) {
			b["reason"] = json.RawMessage(`"retries"`)
			st["block"] = encodeObject(b)
		}
	}
	if tr, ok := objectOf(st["test_review"]); ok {
		for _, role := range []string{"tester", "reviewer"} {
			if r, ok := objectOf(tr[role]); ok {
				delete(r, "restarts")
				tr[role] = encodeObject(r)
			}
		}
		st["test_review"] = encodeObject(tr)
	}

	rec["state"] = encodeObject(st)
	return json.Marshal(rec)
}

// wallPerTry upgrades a version 2 group record: wall_extended becomes the try it was granted on, and a group blocked on
// wall starts a fresh try so a plain run continues it.
func wallPerTry(old []byte) ([]byte, error) {
	rec, ok := objectOf(old)
	if !ok {
		return old, nil
	}

	st, ok := objectOf(rec["state"])
	if !ok {
		return old, nil
	}

	c, ok := objectOf(st["counters"])
	if !ok {
		return old, nil
	}

	extended := string(c["wall_extended"]) == "true"
	delete(c, "wall_extended")
	c["tries"] = json.RawMessage("1")
	c["wall_base_s"] = json.RawMessage("0")
	c["wall_ext_try"] = json.RawMessage("0")
	c["fault_streak"] = json.RawMessage("0")
	if extended {
		c["wall_ext_try"] = json.RawMessage("1")
	}
	if b, ok := objectOf(st["block"]); ok && string(b["reason"]) == `"wall"` {
		if used, ok := c["wall_used_s"]; ok {
			c["wall_base_s"] = used
		}
		c["wall_ext_try"] = json.RawMessage("0")
	}

	st["counters"] = encodeObject(c)
	rec["state"] = encodeObject(st)
	return json.Marshal(rec)
}

// qaRename upgrades a version 3 group record: the tester's test and review result, block reason and block count move
// to qa.
func qaRename(old []byte) ([]byte, error) {
	rec, ok := objectOf(old)
	if !ok {
		return old, nil
	}

	st, ok := objectOf(rec["state"])
	if !ok {
		return old, nil
	}
	if tr, ok := objectOf(st["test_review"]); ok {
		if r, ok := tr["tester"]; ok {
			tr["qa"] = r
			delete(tr, "tester")
			st["test_review"] = encodeObject(tr)
		}
	}
	if b, ok := objectOf(st["block"]); ok && string(b["reason"]) == `"tester"` {
		b["reason"] = json.RawMessage(`"qa"`)
		st["block"] = encodeObject(b)
	}
	if c, ok := objectOf(st["counters"]); ok {
		var blocks map[string]int
		if json.Unmarshal(c["blocks"], &blocks) == nil && blocks != nil {
			if n, ok := blocks["tester"]; ok {
				blocks["qa"] += n
				delete(blocks, "tester")
				c["blocks"], _ = json.Marshal(blocks)
				st["counters"] = encodeObject(c)
			}
		}
	}

	rec["state"] = encodeObject(st)
	return json.Marshal(rec)
}

// spentReasons are the version 1 block reasons that stood for a spent counter, as stored.
var spentReasons = []string{`"returns"`, `"attempts"`, `"retries"`, `"regression"`}

// migrateCounters drops the old counters, starts the budget at the limit and folds the blocks of the old reasons into
// retries.
func migrateCounters(c map[string]json.RawMessage) {
	for _, k := range []string{"returns", "attempts", "retries"} {
		delete(c, k)
	}
	if _, ok := c["retries_left"]; !ok {
		c["retries_left"] = json.RawMessage(strconv.Itoa(migratedRetries))
	}
	if _, ok := c["overrides"]; !ok {
		c["overrides"] = json.RawMessage("0")
	}

	var blocks map[string]int
	if json.Unmarshal(c["blocks"], &blocks) != nil || blocks == nil {
		return
	}
	for _, k := range []string{"returns", "attempts", "regression"} {
		if n, ok := blocks[k]; ok {
			blocks["retries"] += n
			delete(blocks, k)
		}
	}

	b, _ := json.Marshal(blocks)
	c["blocks"] = b
}

// objectOf decodes a JSON object into its raw members; false when raw is not an object.
func objectOf(raw []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// encodeObject encodes raw members as a JSON object; the members are already valid JSON.
func encodeObject(m map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

// migrate upgrades record bytes of the given scope from version from to Version.
func migrate(scope string, data []byte, from int) ([]byte, error) {
	migrationMu.RLock()
	defer migrationMu.RUnlock()
	out, _, err := runMigrations(migrations, scope, Version, from, data)
	return out, err
}

// runMigrations applies the steps from version from up to version current, setting v after each one.
func runMigrations(reg map[migrationKey]Migration, scope string, current, from int, data []byte) ([]byte, int, error) {
	if from < 0 {
		return nil, from, fmt.Errorf("version %d is not valid", from)
	}

	for v := from; v < current; v++ {
		m, ok := reg[migrationKey{scope, v}]
		if !ok {
			return nil, v, fmt.Errorf("no migration from version %d", v)
		}

		out, err := m.Step(data)
		if err != nil {
			return nil, v, fmt.Errorf("migrate from version %d: %w", v, err)
		}
		if data, err = setVersion(out, v+1); err != nil {
			return nil, v, err
		}
	}

	return data, current, nil
}

// setVersion sets the top-level v field of record JSON.
func setVersion(data []byte, v int) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("read migrated record: %w", err)
	}
	m["v"] = json.RawMessage(fmt.Sprint(v))
	return json.Marshal(m)
}
