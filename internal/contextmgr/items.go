package contextmgr

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// Origin says where an item's text comes from.
type Origin string

// The two origins of an item.
const (
	OriginEmbedded Origin = "embedded"
	OriginLocal    Origin = "local"
)

// ItemState is the on/off state and per-agent access of one skill.
type ItemState struct {
	Name    SkillName
	Origin  Origin
	Enabled bool     // a disabled skill is never selected and cannot be pulled
	Allow   []string // agents that may be given or pull it
	Always  []string // agents whose every brief carries it
}

// item is one row of the item table: its state, index row and fingerprint.
type item struct {
	state ItemState
	entry IndexEntry // description, globs, markers, sections and the full fingerprint
	path  string     // the SKILL.md file of a local skill; empty for an embedded one
}

// SkillSet is the data of skill / set: it replaces one skill's state.
type SkillSet struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow"`
	Always  []string `json:"always"`
}

// SkillReload is the data of skill / reload: it rereads the local skills.
type SkillReload struct{}

// MCPSet is the data of mcp / set: it replaces one connection's state.
type MCPSet struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow"`
}

// MCPReload is the data of mcp / reload: it rereads connections.json.
type MCPReload struct{}

// ContextRefused is the data of error / context-refused: a skill or mcp event that failed
// validation.
type ContextRefused struct {
	Event  string `json:"event"`  // the refused event as type/subtype
	Field  string `json:"field"`  // the field that failed
	Reason string `json:"reason"` // what was wrong with it
}

// EventKey names the skill / set pair.
func (SkillSet) EventKey() bus.Key { return bus.Key{Type: bus.TypeSkill, Subtype: "set"} }

// EventKey names the skill / reload pair.
func (SkillReload) EventKey() bus.Key { return bus.Key{Type: bus.TypeSkill, Subtype: "reload"} }

// EventKey names the mcp / set pair.
func (MCPSet) EventKey() bus.Key { return bus.Key{Type: bus.TypeMCP, Subtype: "set"} }

// EventKey names the mcp / reload pair.
func (MCPReload) EventKey() bus.Key { return bus.Key{Type: bus.TypeMCP, Subtype: "reload"} }

// EventKey names the error / context-refused pair.
func (ContextRefused) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeError, Subtype: "context-refused"}
}

// startItems builds the item table: shipped rows, local rows, then the harness's starting state in
// its order.
func (m *Manager) startItems(idx Index) error {
	shipped := map[SkillName]bool{}

	for _, e := range idx.Skills {
		shipped[e.Name] = true
		m.items = append(m.items, &item{
			state: ItemState{
				Name:   e.Name,
				Origin: OriginEmbedded,
				Allow:  []string{},
				Always: []string{},
			},
			entry: e,
		})
	}

	local, warns := loadLocalSkills(m.deps.Home, shipped, m.phrases(), m.count)
	m.items = append(m.items, local...)
	m.skillWarns = warns

	var first []*item

	for _, st := range m.deps.Items {
		it := m.find(st.Name)
		if it == nil {
			return fmt.Errorf("starting state for %q: %w", st.Name, ErrSkillUnknown)
		}

		if slices.Contains(first, it) {
			return fmt.Errorf("starting state names %q twice", st.Name)
		}

		if field, reason := checkAccess(st.Allow, st.Always); field != "" {
			return fmt.Errorf("starting state for %q: %s %s", st.Name, field, reason)
		}

		it.state.Enabled = st.Enabled
		it.state.Allow, it.state.Always = nonNil(st.Allow), nonNil(st.Always)
		first = append(first, it)
	}

	for _, it := range m.items {
		if !slices.Contains(first, it) {
			first = append(first, it)
		}
	}

	m.items = first

	return nil
}

// reloadLocal rereads the local skills in place: a skill still there keeps its row and state, a
// new one loads disabled at the end.
func (m *Manager) reloadLocal() {
	shipped := map[SkillName]bool{}

	for _, it := range m.items {
		if it.state.Origin == OriginEmbedded {
			shipped[it.state.Name] = true
		}
	}

	local, warns := loadLocalSkills(m.deps.Home, shipped, m.phrases(), m.count)
	fresh := map[SkillName]*item{}

	for _, it := range local {
		fresh[it.state.Name] = it
	}

	out := make([]*item, 0, len(m.items))

	for _, it := range m.items {
		if it.state.Origin == OriginEmbedded {
			out = append(out, it)
		} else if f, ok := fresh[it.state.Name]; ok {
			f.state = it.state
			out = append(out, f)

			delete(fresh, it.state.Name)
		}
	}

	for _, it := range local {
		if _, ok := fresh[it.state.Name]; ok {
			out = append(out, it)
		}
	}

	m.items, m.skillWarns = out, warns
}

// phrases returns the current steering phrases.
func (m *Manager) phrases() []string {
	if m.deps.Phrases == nil {
		return nil
	}

	return m.deps.Phrases()
}

// apply validates one skill or mcp event and changes the table; a bad event publishes
// context-refused and changes nothing.
func (m *Manager) apply(ev bus.Event) {
	m.mu.Lock()
	refused := m.applyLocked(ev)
	m.mu.Unlock()

	if refused == nil || m.deps.Bus == nil {
		return
	}

	_ = m.deps.Bus.Publish(bus.Event{
		Type:    bus.TypeError,
		Subtype: "context-refused",
		Run:     ev.Run,
		Group:   ev.Group,
		Data:    refused,
	})
}

// applyLocked changes the table under the lock and returns the refusal to publish, if any.
func (m *Manager) applyLocked(ev bus.Event) *ContextRefused {
	name := string(ev.Type) + "/" + string(ev.Subtype)

	refuse := func(field, reason string) *ContextRefused {
		return &ContextRefused{Event: name, Field: field, Reason: reason}
	}

	switch {
	case ev.Type == bus.TypeSkill && ev.Subtype == "set":
		var d SkillSet
		if err := decodeData(ev.Data, &d); err != nil {
			return refuse("data", err.Error())
		}

		it := m.find(SkillName(d.Name))
		if it == nil {
			return refuse("name", fmt.Sprintf("%q is not a skill in the table", d.Name))
		}

		if field, reason := checkAccess(d.Allow, d.Always); field != "" {
			return refuse(field, reason)
		}

		it.state.Enabled = d.Enabled
		it.state.Allow, it.state.Always = nonNil(d.Allow), nonNil(d.Always)
	case ev.Type == bus.TypeSkill && ev.Subtype == "reload":
		m.reloadLocal()
	case ev.Type == bus.TypeMCP && ev.Subtype == "set":
		var d MCPSet
		if err := decodeData(ev.Data, &d); err != nil {
			return refuse("data", err.Error())
		}

		i := m.connAt(d.Name)

		switch {
		case d.Name == komodoConnection:
			return refuse("name", "the embedded komodo entry is always on and cannot be changed")
		case i < 0:
			return refuse("name", fmt.Sprintf("%q is not a connection in the table", d.Name))
		case badAgent(d.Allow) != "":
			return refuse("allow", fmt.Sprintf("%q is not one of the 4 agents", badAgent(d.Allow)))
		}

		m.conns[i].Enabled, m.conns[i].Allow = d.Enabled, nonNil(d.Allow)
	case ev.Type == bus.TypeMCP && ev.Subtype == "reload":
		m.reloadConnections()
	}

	return nil
}

// RegisterEvents binds the skill, mcp and context-refused subtypes to their data structs.
func RegisterEvents() {
	bus.Register(SkillSet{}.EventKey(), func() bus.Payload { return &SkillSet{} })
	bus.Register(SkillReload{}.EventKey(), func() bus.Payload { return &SkillReload{} })
	bus.Register(MCPSet{}.EventKey(), func() bus.Payload { return &MCPSet{} })
	bus.Register(MCPReload{}.EventKey(), func() bus.Payload { return &MCPReload{} })
	bus.Register(ContextRefused{}.EventKey(), func() bus.Payload { return &ContextRefused{} })
}

// checkAccess returns the field and reason of the first bad value in an allow and always pair.
func checkAccess(allow, always []string) (field, reason string) {
	if bad := badAgent(allow); bad != "" {
		return "allow", fmt.Sprintf("%q is not one of the 4 agents", bad)
	}

	for _, a := range always {
		if !slices.Contains(allow, a) {
			return "always", fmt.Sprintf("%q is not in allow", a)
		}
	}

	return "", ""
}

// decodeData copies an event body into dst, whether the body is dst's own type or another type
// with the same fields.
func decodeData[T any](p bus.Payload, dst *T) error {
	if v, ok := any(p).(*T); ok && v != nil {
		*dst = *v

		return nil
	}

	b, err := json.Marshal(p)
	if err != nil {
		return err
	}

	return decodeStrict(b, dst)
}
