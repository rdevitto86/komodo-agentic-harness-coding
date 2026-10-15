package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

// Known holds the names the role definitions are cross-checked against.
type Known struct {
	GuardRows map[string]bool // permission rows the Task Manager defines
	Tools     map[string]bool // tools the tool runtime registers
}

// Manager reads and writes the settings file and the agent registry.
type Manager struct {
	home    string
	known   Known
	modelOK func(model string) bool
	specs   []KeySpec
	byName  map[string]int // spec index by name
	famIdx  []int          // spec indexes of the family keys
	roles   map[string]Agent
}

// DefaultHome returns KOMODO_HOME when set and ~/.komodo otherwise.
func DefaultHome() (string, error) {
	if h := os.Getenv("KOMODO_HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home folder: %w", err)
	}
	return filepath.Join(h, ".komodo"), nil
}

// New builds the manager. known holds the names the role files are checked against;
// modelOK reports whether a model name passed the provider probe. An empty home means DefaultHome.
func New(home string, known Known, modelOK func(model string) bool) (*Manager, error) {
	embedded, err := fs.Sub(komodo.Embedded, "embed")
	if err != nil {
		return nil, fmt.Errorf("open embedded files: %w", err)
	}
	return newManager(home, embedded, known, modelOK)
}

// newManager builds a manager over the given built-in files.
func newManager(home string, builtin fs.FS, known Known, modelOK func(string) bool) (*Manager, error) {
	if home == "" {
		var err error
		if home, err = DefaultHome(); err != nil {
			return nil, err
		}
	}
	specs, err := loadSpecs(builtin)
	if err != nil {
		return nil, err
	}
	roles, err := loadRoles(builtin, known)
	if err != nil {
		return nil, err
	}
	m := &Manager{home: home, known: known, modelOK: modelOK, specs: specs, byName: map[string]int{}, roles: roles}
	for i, s := range specs {
		m.byName[s.Name] = i
		if s.Family {
			m.famIdx = append(m.famIdx, i)
		}
	}
	return m, nil
}

// GetConfigItem returns one key with its source, default and limit.
func (m *Manager) GetConfigItem(key string) (Item, error) {
	return m.item(key, m.resolve())
}

// ListConfigItems returns every key under a prefix; an empty prefix returns all.
func (m *Manager) ListConfigItems(prefix string) []Item {
	r := m.resolve()
	var keys []string
	for _, s := range m.specs {
		keys = append(keys, s.Name)
	}
	for k := range r.ms.flat {
		if _, listed := m.byName[k]; !listed {
			keys = append(keys, k)
		}
	}
	for _, role := range roleNames {
		for _, f := range agentFieldNames {
			keys = append(keys, "agents."+role+"."+f)
		}
	}
	sort.Strings(keys)
	var out []Item
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if it, err := m.item(k, r); err == nil {
			out = append(out, it)
		}
	}
	return out
}

// GetAgent returns one role's definition with its overrides applied.
func (m *Manager) GetAgent(role string) (Agent, bool) {
	a, ok := m.resolve().eff.Agents[role]
	return a, ok
}

// Snapshot returns the effective settings and their hash for a run to pin.
// It fails when a machine file cannot be read strictly or a role file fails a cross-check.
func (m *Manager) Snapshot() (Snapshot, error) {
	if err := checkRoles(m.roles, m.known); err != nil {
		return Snapshot{}, err
	}
	r := m.resolve()
	if r.readErr != nil {
		return Snapshot{}, r.readErr
	}
	h, err := hashEffective(r.eff)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Effective: r.eff, Hash: h}, nil
}

// PlanReset returns the changes that put a key, a role or a whole file back to its built-in value.
// A target is a key, a role name, agents.<role>, settings.json or agents.json.
func (m *Manager) PlanReset(target string) ([]Change, error) {
	r := m.resolve()
	if r.readErr != nil {
		return nil, r.readErr
	}
	var keys []string
	role := strings.TrimPrefix(target, "agents.")
	switch {
	case target == "settings.json":
		for k := range r.ms.flat {
			keys = append(keys, k)
		}
	case target == "agents.json":
		for _, ro := range roleNames {
			keys = append(keys, overrideKeys(ro, r.ms.over[ro])...)
		}
	case slices.Contains(roleNames, role):
		keys = overrideKeys(role, r.ms.over[role])
	default:
		keys = []string{target}
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		it, err := m.item(k, r)
		if err != nil {
			return nil, err
		}
		if !equalValues(it.Value, it.Default) {
			out = append(out, Change{Key: k, Old: it.Value, New: it.Default})
		}
	}
	return out, nil
}

// overrideKeys lists the dotted keys a role's override sets.
func overrideKeys(role string, o AgentOverride) []string {
	var keys []string
	for _, f := range agentFieldNames {
		if overrideValue(o, f) != nil {
			keys = append(keys, "agents."+role+"."+f)
		}
	}
	return keys
}

// marshalFile encodes a machine file as indented JSON with a closing newline.
func marshalFile(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode machine file: %w", err)
	}
	return append(b, '\n'), nil
}
