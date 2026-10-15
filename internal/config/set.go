package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// target is one checked pair of a SetConfigItem call.
type target struct {
	key   string
	role  string // set for an agents.<role>.<field> key
	field string
	spec  KeySpec
	val   any    // the normalised new value; nil puts the key back to its built-in value
	file  string // set for a machine file name, which rewrites that file to its readable contents
}

// modelFix is the limit text of a model refused by the probe check.
const modelFix = "a model found by the probe; run komodo install --machine-only"

// SetConfigItem checks every change and writes all of them or none.
// A change whose value equals the effective value is dropped, and a New of nil resets the key.
func (m *Manager) SetConfigItem(changes ...Change) (Batch, error) {
	targets, err := m.prepare(changes)
	if err != nil || len(targets) == 0 {
		return Batch{}, err
	}
	unlock, err := m.lock()
	if err != nil {
		return Batch{}, err
	}
	defer unlock()
	if bad, err := m.tamperedNow(); err != nil {
		return Batch{}, err
	} else if bad {
		return Batch{}, ErrTampered
	}
	pre := m.resolve()
	if pre.readErr != nil {
		pre = resolved{ms: pre.partial, eff: m.effective(pre.partial)}
	}
	ms := machine{flat: maps.Clone(pre.ms.flat), over: maps.Clone(pre.ms.over), bad: maps.Clone(pre.ms.bad)}
	for _, t := range targets {
		if t.file != "" {
			ms.bad[t.file] = true
		} else if t.role == "" {
			m.applyFlat(&ms, t)
		}
	}
	flat := m.effective(ms).Flat
	for _, t := range targets {
		if t.role != "" && t.file == "" {
			ms.over[t.role] = m.applyOverride(ms.over[t.role], t, flat)
		}
	}
	post := resolved{ms: ms, eff: m.effective(ms)}
	if err := m.checkMaxKeys(post.eff.Flat); err != nil {
		return Batch{}, err
	}
	b := newBatch()
	for _, t := range targets {
		if t.file != "" {
			continue
		}
		o, _ := m.item(t.key, pre)
		n, _ := m.item(t.key, post)
		if !equalValues(o.Value, n.Value) {
			b.Changes = append(b.Changes, Change{Key: t.key, Old: o.Value, New: n.Value})
		}
	}
	if len(b.Changes) == 0 && len(ms.bad) == 0 {
		return Batch{}, nil
	}
	if len(b.Changes) == 0 {
		return Batch{}, m.commit(Batch{}, ms)
	}
	return b, m.commit(b, ms)
}

// prepare finds the spec of each pair and checks its value; the last pair for a key wins.
func (m *Manager) prepare(changes []Change) ([]target, error) {
	var out []target
	for _, c := range changes {
		t, err := m.target(c)
		if err != nil {
			return nil, err
		}
		replaced := false
		for i := range out {
			if out[i].key == t.key {
				out[i], replaced = t, true
			}
		}
		if !replaced {
			out = append(out, t)
		}
	}
	return out, nil
}

// target checks one pair against the key table or the four override keys.
func (m *Manager) target(c Change) (target, error) {
	t := target{key: c.Key}
	if slices.Contains(machineFiles, c.Key) && c.New == nil {
		t.file = c.Key
		return t, nil
	}
	if role, field, ok := splitAgentKey(c.Key); ok {
		if !slices.Contains(roleNames, role) || !slices.Contains(agentFieldNames, field) {
			return t, m.unknownKey(c.Key)
		}
		t.role, t.field, t.spec = role, field, agentSpec(role, field)
	} else {
		spec, ok := m.lookup(c.Key)
		if !ok {
			return t, m.unknownKey(c.Key)
		}
		t.spec = spec
	}
	if c.New == nil {
		return t, nil
	}
	v, err := checkValue(t.spec, c.New, true)
	if err != nil {
		return t, err
	}
	if t.field == "model" {
		model := v.(string)
		if strings.TrimSpace(model) == "" {
			return t, BadValueError{Key: c.Key, Value: model, Detail: "want a model name"}
		}
		if m.modelOK == nil || !m.modelOK(model) {
			return t, LimitError{Key: c.Key, Value: model, Limit: modelFix}
		}
	}
	t.val = v
	return t, nil
}

// applyFlat sets or removes one settings.json key; a value equal to the built-in one is removed.
func (m *Manager) applyFlat(ms *machine, t target) {
	if t.val == nil || equalValues(t.val, t.spec.Default) {
		delete(ms.flat, t.key)
		return
	}
	ms.flat[t.key] = t.val
}

// applyOverride sets or clears one override field; a value equal to the unoverridden one is cleared.
func (m *Manager) applyOverride(o AgentOverride, t target, flat map[string]Setting) AgentOverride {
	if t.val != nil && t.field != "model" && equalValues(t.val, limitField(m.roleLimits(t.role, flat, AgentOverride{}), t.field)) {
		return withOverride(o, t.field, nil)
	}
	return withOverride(o, t.field, t.val)
}

// commit writes a batch: audit rows, then the changed files, then the checksums.
func (m *Manager) commit(b Batch, ms machine) error {
	if err := m.appendRows(b); err != nil {
		return err
	}
	crashHook("after_audit")
	settings, agents := ms.bad["settings.json"], ms.bad["agents.json"]
	for _, c := range b.Changes {
		if strings.HasPrefix(c.Key, "agents.") {
			agents = true
		} else {
			settings = true
		}
	}
	if settings {
		data, err := marshalFile(ms.flat)
		if err != nil {
			return err
		}
		if err := m.writeAtomic(filepath.Join(m.home, "settings.json"), data); err != nil {
			return fmt.Errorf("write settings.json: %w", err)
		}
	}
	if agents {
		over := map[string]AgentOverride{}
		for role, o := range ms.over {
			if o != (AgentOverride{}) {
				over[role] = o
			}
		}
		data, err := marshalFile(over)
		if err != nil {
			return err
		}
		if err := m.writeAtomic(filepath.Join(m.home, "agents.json"), data); err != nil {
			return fmt.Errorf("write agents.json: %w", err)
		}
	}
	crashHook("after_files")
	return m.writeSums()
}
