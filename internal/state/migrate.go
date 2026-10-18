package state

import (
	"encoding/json"
	"fmt"
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
const Version = 1

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

// Version 0 has the same shape as version 1, so its step changes nothing.
func init() {
	identity := func(old []byte) ([]byte, error) { return old, nil }
	RegisterMigration(Migration{From: 0, Step: identity})
	for _, k := range machineKinds {
		RegisterMigration(Migration{From: 0, Kind: k, Step: identity})
	}
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
