package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Effective is the merged settings and role definitions.
type Effective struct {
	Flat   map[string]Setting
	Agents map[string]Agent // each role with its overrides applied
}

// Snapshot is the effective settings a run copies at its start.
type Snapshot struct {
	Effective
	Hash string // SHA-256 in lowercase hexadecimal
}

// Item is one key as GetConfigItem returns it.
type Item struct {
	Key      string
	Value    any
	Source   Layer
	Default  any
	Limit    any      // the hard limit that applies, nil when none
	Allowed  []string // the allowed values for an enum key
	Fallback bool     // true when the machine layer was unreadable and the built-in value is served
}

// ReadError reports a machine file that failed strict reading.
type ReadError struct {
	File string
	Err  error
}

// machine holds the decoded contents of the two machine files.
type machine struct {
	flat map[string]any           // settings.json: key to normalised value
	over map[string]AgentOverride // agents.json: role to override
	// machine files that failed strict reading and are rewritten by the next write
	bad map[string]bool
}

// resolved is the effective settings with the machine files they came from.
type resolved struct {
	ms      machine
	eff     Effective
	readErr error   // set when a machine file failed strict reading; eff is then the built-in layer
	partial machine // what the readable files hold when readErr is set
}

// Error names the file and the failure; an unknown key adds the update fix.
func (e ReadError) Error() string {
	var uk UnknownKeyError
	if errors.As(e.Err, &uk) {
		return fmt.Sprintf(
			"read %s: %v; if a newer komodo wrote this file, run komodo update",
			e.File,
			e.Err,
		)
	}

	return fmt.Sprintf("read %s: %v", e.File, e.Err)
}

// Unwrap returns the underlying failure.
func (e ReadError) Unwrap() error { return e.Err }

// resolve reads the machine files and merges them over the built-in layer.
// When a file fails strict reading, the built-in layer is served and the readable files are kept in
// partial.
func (m *Manager) resolve() resolved {
	ms, eff, err := m.readMachine()
	if err != nil {
		return resolved{
			ms:      emptyMachine(),
			eff:     m.effective(emptyMachine()),
			partial: ms,
			readErr: err,
		}
	}

	return resolved{ms: ms, eff: eff}
}

// readMachine reads settings.json and agents.json strictly; an absent file holds no values.
// It returns what the readable files hold, their effective settings and the first read failure.
func (m *Manager) readMachine() (machine, Effective, error) {
	ms := emptyMachine()

	var first error

	fail := func(file string, err error) {
		ms.bad[file] = true
		if first == nil {
			first = ReadError{File: file, Err: err}
		}
	}
	if b, err := readIfExists(filepath.Join(m.home, "settings.json")); err != nil {
		fail("settings.json", err)
	} else if b != nil {
		if flat, err := m.decodeFlat(b); err != nil {
			fail("settings.json", err)
		} else {
			ms.flat = flat
		}
	}

	if b, err := readIfExists(filepath.Join(m.home, "agents.json")); err != nil {
		fail("agents.json", err)
	} else if b != nil {
		if over, err := m.readOverrides(b); err != nil {
			fail("agents.json", err)
		} else {
			ms.over = over
		}
	}

	eff := m.effective(ms)
	if !ms.bad["settings.json"] {
		if err := m.checkMaxKeys(eff.Flat); err != nil {
			fail("settings.json", err)

			ms.flat = map[string]any{}
			eff = m.effective(ms)
		}
	}

	return ms, eff, first
}

// decodeFlat decodes settings.json: every top-level key must be a table key or a family member.
func (m *Manager) decodeFlat(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()

	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("decode: data after the settings object")
	}

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	out := make(map[string]any, len(raw))

	for _, k := range keys {
		spec, ok := m.lookup(k)
		if !ok {
			return nil, m.unknownKey(k)
		}

		v, err := checkValue(spec, raw[k], false)
		if err != nil {
			return nil, err
		}

		out[k] = v
	}

	return out, nil
}

// checkMaxKeys fails when a key is above the key that caps it.
func (m *Manager) checkMaxKeys(flat map[string]Setting) error {
	for _, s := range m.specs {
		if s.MaxKey == "" {
			continue
		}

		v, hi := flat[s.Name].Value, flat[s.MaxKey].Value
		if v != nil && hi != nil && mustFloat(v) > mustFloat(hi) {
			return LimitError{Key: s.Name, Value: v, Limit: hi}
		}
	}

	return nil
}

// effective overlays the machine values on the built-in layer and applies each role's overrides.
func (m *Manager) effective(ms machine) Effective {
	flat := make(map[string]Setting, len(m.specs)+len(ms.flat))
	for _, s := range m.specs {
		if !s.Family {
			flat[s.Name] = Setting{Value: s.Default, Source: LayerBuiltIn}
		}
	}

	for k, v := range ms.flat {
		flat[k] = Setting{Value: v, Source: LayerMachine}
	}

	agents := make(map[string]Agent, len(m.roles))
	for role, a := range m.roles {
		a = cloneAgent(a)
		a.Limits = m.roleLimits(role, flat, ms.over[role])
		agents[role] = a
	}

	return Effective{Flat: flat, Agents: agents}
}

// item builds the Item of one key from a resolved state.
func (m *Manager) item(key string, r resolved) (Item, error) {
	it := Item{Key: key, Fallback: r.readErr != nil}
	if role, field, ok := splitAgentKey(key); ok {
		return m.agentItem(it, role, field, r)
	}

	spec, ok := m.lookup(key)
	if !ok {
		return Item{}, m.unknownKey(key)
	}

	it.Default, it.Allowed, it.Limit = spec.Default, spec.Enum, spec.Max
	if set, found := r.eff.Flat[key]; found {
		it.Value, it.Source = set.Value, set.Source
	} else {
		it.Value, it.Source = spec.Default, LayerBuiltIn
	}

	if it.Limit == nil && spec.MaxKey != "" {
		it.Limit = r.eff.Flat[spec.MaxKey].Value
	}

	return it, nil
}

// agentItem fills the Item of an agents.<role>.<field> key.
func (m *Manager) agentItem(it Item, role, field string, r resolved) (Item, error) {
	if _, known := m.roles[role]; !known {
		return Item{}, m.unknownKey(it.Key)
	}

	spec, ok := agentFields[field]
	if !ok {
		return Item{}, m.unknownKey(it.Key)
	}

	it.Limit = spec.Max
	over := r.ms.over[role]

	it.Source = LayerBuiltIn
	if field == "model" {
		it.Default = "weight:" + string(m.roles[role].Weight)

		it.Value = it.Default
		if v := overrideValue(over, field); v != nil {
			it.Value, it.Source = v, LayerMachine
		}

		return it, nil
	}

	def := limitField(m.roleLimits(role, r.eff.Flat, AgentOverride{}), field)
	val := limitField(m.roleLimits(role, r.eff.Flat, over), field)

	it.Default, it.Value = def, val
	fromMachine := field != "retries" && r.eff.Flat["limits."+field].Source == LayerMachine

	if overrideValue(over, field) != nil || fromMachine {
		it.Source = LayerMachine
	}

	return it, nil
}

// emptyMachine returns a machine state with no values.
func emptyMachine() machine {
	return machine{flat: map[string]any{}, over: map[string]AgentOverride{}, bad: map[string]bool{}}
}

// readIfExists returns a file's bytes, or nil when it does not exist.
func readIfExists(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err == nil && b == nil {
		b = []byte{}
	}

	return b, err
}

// hashEffective returns the SHA-256 of the effective values without their sources.
func hashEffective(e Effective) (string, error) {
	flat := make(map[string]any, len(e.Flat))
	for k, s := range e.Flat {
		flat[k] = s.Value
	}

	b, err := json.Marshal(struct {
		Flat   map[string]any
		Agents map[string]Agent
	}{flat, e.Agents})
	if err != nil {
		return "", fmt.Errorf("encode settings for hashing: %w", err)
	}

	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:]), nil
}

// limitField reads one clock of a Limits value by its override key name.
func limitField(l Limits, field string) any {
	switch field {
	case "session_wall_min":
		return l.WallMin
	case "session_idle_min":
		return l.IdleMin
	}

	return l.Retries
}
