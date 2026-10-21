package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
)

// Weight is an agent's model size; it picks the model per link.
type Weight string

const (
	Heavy    Weight = "heavy"
	Standard Weight = "standard"
	Light    Weight = "light"
)

// Limits are one agent's session clocks in minutes.
type Limits struct {
	WallMin int `json:"wall_min"` // most minutes a session may run
	IdleMin int `json:"idle_min"` // minutes of silence before the nudge
}

// Skills lists the skills an agent may pull and the ones injected into every brief.
type Skills struct {
	Allow  []string `json:"allow"`
	Always []string `json:"always"`
}

// Slots lists the brief slots an agent must receive and those it may receive.
type Slots struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

// Agent is the decoded agent.json of one role.
type Agent struct {
	V      int      `json:"v"`      // schema version, 1
	Weight Weight   `json:"weight"` // heavy, standard or light
	Chain  []string `json:"chain"`  // model links in order
	Tools  []string `json:"tools"`  // the only tools advertised to the session
	// the only command classes the run tool may execute
	RunClasses []string        `json:"run_classes"`
	Guard      []string        `json:"guard"` // permission rows beyond the inherited ones
	Limits     Limits          `json:"limits"`
	Skills     Skills          `json:"skills"`
	Slots      Slots           `json:"slots"`
	Result     json.RawMessage `json:"result"`    // JSON Schema of the final call
	PoolSlot   bool            `json:"pool_slot"` // takes one session slot
}

// AgentOverride is one role's entry in agents.json; nil fields keep the built-in value.
type AgentOverride struct {
	Model          string `json:"model,omitempty"`
	SessionWallMin *int   `json:"session_wall_min,omitempty"`
	SessionIdleMin *int   `json:"session_idle_min,omitempty"`
}

var (
	// roleNames lists the four roles; the registry holds no others.
	roleNames = []string{"planner", "builder", "qa", "reviewer"}

	// validLinks lists the model links a role chain may name.
	validLinks = []string{"claude-cli", "anthropic-api", "openai-subscription", "openai-api", "local"}

	// agentFieldNames lists the four override keys a person may set per role.
	agentFieldNames = []string{"model", "session_wall_min", "session_idle_min"}

	// agentFields holds the kind and hard limits of each override key.
	agentFields = map[string]KeySpec{
		"model":            {Kind: KindString},
		"session_wall_min": {Kind: KindInt, Min: 1, Max: 120},
		"session_idle_min": {Kind: KindInt, Min: 1, Max: 30},
	}
)

// roleLimits applies the layer order to a role's clocks: override, then a machine flat key, then
// the role file.
func (m *Manager) roleLimits(role string, flat map[string]Setting, over AgentOverride) Limits {
	l := m.roles[role].Limits
	if s := flat["limits.session_wall_min"]; s.Source == LayerMachine {
		l.WallMin = s.Value.(int)
	}
	if s := flat["limits.session_idle_min"]; s.Source == LayerMachine {
		l.IdleMin = s.Value.(int)
	}

	if over.SessionWallMin != nil {
		l.WallMin = *over.SessionWallMin
	}
	if over.SessionIdleMin != nil {
		l.IdleMin = *over.SessionIdleMin
	}

	l.WallMin = min(l.WallMin, agentFields["session_wall_min"].Max.(int))
	l.IdleMin = min(l.IdleMin, agentFields["session_idle_min"].Max.(int))

	return l
}

// readOverrides decodes agents.json strictly: a role outside the four or a field outside the four
// keys fails.
func (m *Manager) readOverrides(b []byte) (map[string]AgentOverride, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	out := map[string]AgentOverride{}

	for role, body := range raw {
		if !slices.Contains(roleNames, role) {
			return nil, UnknownKeyError{Key: role, Closest: closest(role, roleNames)}
		}

		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()

		var o AgentOverride
		if err := dec.Decode(&o); err != nil {
			return nil, BadValueError{Key: role, Value: string(body), Detail: err.Error()}
		}

		fields := map[string]any{
			"model":            o.Model,
			"session_wall_min": o.SessionWallMin,
			"session_idle_min": o.SessionIdleMin,
		}

		for field, v := range fields {
			if p, isInt := v.(*int); isInt && p != nil {
				if _, err := checkValue(agentSpec(role, field), *p, false); err != nil {
					return nil, err
				}
			}
		}

		out[role] = o
	}

	return out, nil
}

// loadRoles reads the four role files, decodes them strictly and runs the cross-checks.
func loadRoles(fsys fs.FS, known Known) (map[string]Agent, error) {
	entries, err := fs.ReadDir(fsys, "agents")
	if err != nil {
		return nil, fmt.Errorf("read agents folder: %w", err)
	}

	var found []string
	for _, e := range entries {
		found = append(found, e.Name())
	}

	want := slices.Clone(roleNames)
	sort.Strings(want)

	if !slices.Equal(found, want) {
		return nil, fmt.Errorf("agents folder holds %v; want exactly %v", found, want)
	}

	roles := make(map[string]Agent, len(roleNames))
	for _, role := range roleNames {
		b, err := fs.ReadFile(fsys, "agents/"+role+"/agent.json")
		if err != nil {
			return nil, fmt.Errorf("read role %s: %w", role, err)
		}

		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()

		var a Agent
		if err := dec.Decode(&a); err != nil {
			return nil, fmt.Errorf("decode role %s: %w", role, err)
		}

		var compact bytes.Buffer
		if err := json.Compact(&compact, a.Result); err != nil {
			return nil, fmt.Errorf("role %s result schema: %w", role, err)
		}

		a.Result = compact.Bytes()
		roles[role] = a
	}

	if err := checkRoles(roles, known); err != nil {
		return nil, err
	}

	return roles, nil
}

// checkRoles checks every role's version, weight, chain, limits and result id, and the known
// tables. A nil or empty known table skips its check.
func checkRoles(roles map[string]Agent, known Known) error {
	ids := map[string]string{}

	for _, role := range roleNames {
		a := roles[role]
		if a.V != 1 {
			return fmt.Errorf("role %s: schema version %d; want 1", role, a.V)
		}
		if !slices.Contains([]Weight{Heavy, Standard, Light}, a.Weight) {
			return fmt.Errorf("role %s: weight %q is not heavy, standard or light", role, a.Weight)
		}
		if len(a.Chain) == 0 {
			return fmt.Errorf("role %s: chain is empty", role)
		}

		for i, link := range a.Chain {
			if !slices.Contains(validLinks, link) || slices.Contains(a.Chain[:i], link) {
				return fmt.Errorf("role %s: chain link %q is unknown or repeated", role, link)
			}
		}

		if err := checkLimits(role, a.Limits); err != nil {
			return err
		}

		var res struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(a.Result, &res); err != nil {
			return fmt.Errorf("role %s: result schema: %w", role, err)
		}
		if want := fmt.Sprintf("komodo:agent/%s/result/%d", role, a.V); res.ID != want {
			return fmt.Errorf("role %s: result $id %q; want %q", role, res.ID, want)
		}

		if other, dup := ids[res.ID]; dup {
			return fmt.Errorf("roles %s and %s share the result $id %q", other, role, res.ID)
		}

		ids[res.ID] = role
		if err := checkKnown(role, a, known); err != nil {
			return err
		}
	}

	return nil
}

// checkLimits checks a role file's clocks against the hard limits of the override keys.
func checkLimits(role string, l Limits) error {
	limits := map[string]int{
		"session_wall_min": l.WallMin,
		"session_idle_min": l.IdleMin,
	}

	for field, v := range limits {
		f := agentFields[field]
		if v < f.Min.(int) || v > f.Max.(int) {
			return fmt.Errorf("role %s: %s %d is outside %d to %d", role, field, v, f.Min, f.Max)
		}
	}

	return nil
}

// checkKnown checks a role's guard rows and tools against the tables other parts supply.
func checkKnown(role string, a Agent, known Known) error {
	if len(known.GuardRows) > 0 {
		for _, g := range a.Guard {
			if !known.GuardRows[g] {
				return fmt.Errorf("role %s: guard row %s is not in the permission table", role, g)
			}
		}
	}

	if len(known.Tools) > 0 {
		for _, t := range a.Tools {
			if !known.Tools[t] {
				return fmt.Errorf("role %s: tool %s is not registered", role, t)
			}
		}
	}

	return nil
}

// cloneAgent copies an agent so a caller cannot change the shared definition.
func cloneAgent(a Agent) Agent {
	a.Chain = slices.Clone(a.Chain)
	a.Tools = slices.Clone(a.Tools)
	a.RunClasses = slices.Clone(a.RunClasses)
	a.Guard = slices.Clone(a.Guard)
	a.Skills.Allow = slices.Clone(a.Skills.Allow)
	a.Skills.Always = slices.Clone(a.Skills.Always)
	a.Slots.Required = slices.Clone(a.Slots.Required)
	a.Slots.Optional = slices.Clone(a.Slots.Optional)
	a.Result = slices.Clone(a.Result)
	return a
}

// splitAgentKey splits agents.<role>.<field> into its parts.
func splitAgentKey(key string) (role, field string, ok bool) {
	rest, found := strings.CutPrefix(key, "agents.")
	if !found {
		return "", "", false
	}
	role, field, ok = strings.Cut(rest, ".")
	return role, field, ok
}

// agentSpec returns the spec of one override key, named by its full dotted key.
func agentSpec(role, field string) KeySpec {
	s := agentFields[field]
	s.Name = "agents." + role + "." + field
	return s
}

// overrideValue reads one field of an override as a normalised value; nil when unset.
func overrideValue(o AgentOverride, field string) any {
	switch field {
	case "model":
		if o.Model != "" {
			return o.Model
		}
	case "session_wall_min":
		if o.SessionWallMin != nil {
			return *o.SessionWallMin
		}
	case "session_idle_min":
		if o.SessionIdleMin != nil {
			return *o.SessionIdleMin
		}
	}

	return nil
}

// withOverride returns o with one field set to v, or cleared when v is nil.
func withOverride(o AgentOverride, field string, v any) AgentOverride {
	ptr := func() *int {
		if v == nil {
			return nil
		}
		i := v.(int)
		return &i
	}

	switch field {
	case "model":
		o.Model, _ = v.(string)
	case "session_wall_min":
		o.SessionWallMin = ptr()
	case "session_idle_min":
		o.SessionIdleMin = ptr()
	}

	return o
}
