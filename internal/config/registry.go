package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is the value type of a key.
type Kind int

const (
	KindString Kind = iota
	KindEnum        // one of KeySpec.Enum
	KindBool
	KindInt
	KindNumber   // integer or decimal
	KindDuration // such as 1h or 10m
	KindPath     // a file path
	KindEnvRef   // env:NAME, never a secret value
)

// kindNames maps the kind spelling in defaults.json to its Kind.
var kindNames = map[string]Kind{
	"string": KindString, "enum": KindEnum, "bool": KindBool, "int": KindInt,
	"number": KindNumber, "duration": KindDuration, "path": KindPath, "env_ref": KindEnvRef,
}

// KeySpec is one row of the built-in table: a key, its default and its hard limit.
type KeySpec struct {
	Name    string
	Kind    Kind
	Default any            // nil when Derived is set
	Enum    []string       // allowed values for KindEnum
	Min     any            // hard minimum; nil when none
	Max     any            // hard maximum; nil when none
	MaxKey  string         // another key that caps this one, such as limits.pool_max
	Derived string         // names the rule when the default is computed per group
	Family  bool           // the name holds one variable part in angle brackets, such as a link name
	Members map[string]any // the variable parts a family allows with their defaults; nil allows any name
}

// Layer names where a value came from.
type Layer int

const (
	LayerBuiltIn Layer = 1 // embed/defaults.json
	LayerMachine Layer = 2 // ~/.komodo/settings.json and ~/.komodo/agents.json
)

// Setting is one resolved value and the layer that set it.
type Setting struct {
	Value  any
	Source Layer
}

// LimitError reports a value over its hard limit, naming the key, the value and the limit.
type LimitError struct {
	Key   string
	Value any
	Limit any
}

// Error names the key, the value and the limit.
func (e LimitError) Error() string {
	return fmt.Sprintf("%s: value %v is outside the hard limit %v", e.Key, e.Value, e.Limit)
}

// UnknownKeyError reports a key or role outside the built-in table and names the closest one.
type UnknownKeyError struct {
	Key     string
	Closest string
}

// Error names the key and the closest known one.
func (e UnknownKeyError) Error() string {
	return fmt.Sprintf("unknown key %q; the closest key is %q", e.Key, e.Closest)
}

// BadValueError reports a value of the wrong type or outside its allowed list.
type BadValueError struct {
	Key    string
	Value  any
	Detail string
}

// Error names the key, the value and what is wrong with it.
func (e BadValueError) Error() string {
	return fmt.Sprintf("%s: bad value %v: %s", e.Key, e.Value, e.Detail)
}

// envRefPattern matches env:NAME, the only form a key setting may take.
var envRefPattern = regexp.MustCompile(`^env:[A-Za-z_][A-Za-z0-9_]*$`)

// rawSpec is one defaults.json row before its kind and numbers are resolved.
type rawSpec struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Default any            `json:"default"`
	Enum    []string       `json:"enum"`
	Min     any            `json:"min"`
	Max     any            `json:"max"`
	MaxKey  string         `json:"max_key"`
	Derived string         `json:"derived"`
	Family  bool           `json:"family"`
	Members map[string]any `json:"members"`
}

// loadSpecs reads and checks the built-in key table from defaults.json.
func loadSpecs(fsys fs.FS) ([]KeySpec, error) {
	b, err := fs.ReadFile(fsys, "defaults.json")
	if err != nil {
		return nil, fmt.Errorf("read defaults.json: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var file struct {
		Keys []rawSpec `json:"keys"`
	}
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode defaults.json: %w", err)
	}
	seen := map[string]bool{}
	specs := make([]KeySpec, 0, len(file.Keys))
	for _, r := range file.Keys {
		kind, ok := kindNames[r.Kind]
		if !ok || r.Name == "" || seen[r.Name] {
			return nil, fmt.Errorf("defaults.json: key %q has a bad kind or a repeated name", r.Name)
		}
		seen[r.Name] = true
		s := KeySpec{Name: r.Name, Kind: kind, Enum: r.Enum, MaxKey: r.MaxKey, Derived: r.Derived, Family: r.Family}
		var err error
		if s.Default, err = specBound(kind, r.Default); err != nil {
			return nil, fmt.Errorf("defaults.json: default of %s: %w", r.Name, err)
		}
		if s.Min, err = specBound(kind, r.Min); err != nil {
			return nil, fmt.Errorf("defaults.json: min of %s: %w", r.Name, err)
		}
		if s.Max, err = specBound(kind, r.Max); err != nil {
			return nil, fmt.Errorf("defaults.json: max of %s: %w", r.Name, err)
		}
		if s.Default != nil {
			if _, err := checkValue(s, s.Default, false); err != nil {
				return nil, fmt.Errorf("defaults.json: %w", err)
			}
		}
		if len(r.Members) > 0 && !r.Family {
			return nil, fmt.Errorf("defaults.json: %s lists members but is not a family", r.Name)
		}
		for name, v := range r.Members {
			def, err := specBound(kind, v)
			if err == nil {
				_, err = checkValue(s, def, false)
			}
			if err != nil {
				return nil, fmt.Errorf("defaults.json: member %s of %s: %w", name, r.Name, err)
			}
			if s.Members == nil {
				s.Members = map[string]any{}
			}
			s.Members[name] = def
		}
		specs = append(specs, s)
	}
	for _, s := range specs {
		if s.MaxKey != "" && !seen[s.MaxKey] {
			return nil, fmt.Errorf("defaults.json: %s names the missing key %s", s.Name, s.MaxKey)
		}
	}
	return specs, nil
}

// specBound turns a decoded table number into the Go type of its kind; other values pass through.
func specBound(kind Kind, v any) (any, error) {
	n, ok := v.(json.Number)
	if !ok {
		return v, nil
	}
	switch kind {
	case KindInt:
		i, err := strconv.Atoi(n.String())
		return i, err
	case KindNumber:
		f, err := n.Float64()
		return f, err
	}
	return nil, fmt.Errorf("number %s on a non-numeric kind", n)
}

// checkValue converts v to the Go type of the key's kind and checks it against the kind and the hard limits.
// With fromText set, a string is parsed as the command line would give it.
func checkValue(s KeySpec, v any, fromText bool) (any, error) {
	bad := func(detail string) error { return BadValueError{Key: s.Name, Value: v, Detail: detail} }
	switch s.Kind {
	case KindString, KindPath:
		str, ok := v.(string)
		if !ok {
			return nil, bad("want a string")
		}
		return str, nil
	case KindEnum:
		str, ok := v.(string)
		if !ok || !slices.Contains(s.Enum, str) {
			return nil, bad("want one of " + strings.Join(s.Enum, ", "))
		}
		return str, nil
	case KindBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			if fromText && (x == "true" || x == "false") {
				return x == "true", nil
			}
		}
		return nil, bad("want true or false")
	case KindInt, KindNumber:
		return checkNumber(s, v, fromText, bad)
	case KindDuration:
		str, ok := v.(string)
		if _, err := time.ParseDuration(str); !ok || err != nil {
			return nil, bad("want a duration such as 10m or 1h")
		}
		return str, nil
	case KindEnvRef:
		str, ok := v.(string)
		if !ok || !envRefPattern.MatchString(str) {
			return nil, bad("want env:NAME; a secret value is never stored")
		}
		return str, nil
	}
	return nil, bad("unknown kind")
}

// checkNumber converts v to int or float64 by kind and applies Min and Max.
func checkNumber(s KeySpec, v any, fromText bool, bad func(string) error) (any, error) {
	f, ok := toFloat(v)
	if str, isStr := v.(string); isStr && fromText {
		var err error
		f, err = strconv.ParseFloat(str, 64)
		ok = err == nil
	}
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, bad("want a number")
	}
	var out any = f
	if s.Kind == KindInt {
		if f != math.Trunc(f) {
			return nil, bad("want a whole number")
		}
		out = int(f)
	}
	if s.Min != nil && f < mustFloat(s.Min) {
		return nil, LimitError{Key: s.Name, Value: out, Limit: s.Min}
	}
	if s.Max != nil && f > mustFloat(s.Max) {
		return nil, LimitError{Key: s.Name, Value: out, Limit: s.Max}
	}
	return out, nil
}

// toFloat reads any number type a caller or the JSON decoder may hand over.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// mustFloat reads a table bound, which loadSpecs already typed as a number.
func mustFloat(v any) float64 {
	f, _ := toFloat(v)
	return f
}

// equalValues reports whether two normalised values are the same.
func equalValues(a, b any) bool { return reflect.DeepEqual(a, b) }

// lookup finds the spec for a key, matching a family by its prefix and tail; a family hit carries the full key as its name.
// A family with members accepts only those members and takes the member's default.
func (m *Manager) lookup(key string) (KeySpec, bool) {
	if i, ok := m.byName[key]; ok && !m.specs[i].Family {
		return m.specs[i], true
	}
	for _, i := range m.famIdx {
		s := m.specs[i]
		member, ok := familyMember(s.Name, key)
		if !ok {
			continue
		}
		if s.Members != nil {
			def, known := s.Members[member]
			if !known {
				continue
			}
			s.Default = def
		}
		s.Name = key
		return s, true
	}
	return KeySpec{}, false
}

// familyMember returns the variable part of key for a family name such as limits.link_sessions.<link>.
func familyMember(family, key string) (string, bool) {
	open, end := strings.Index(family, "<"), strings.Index(family, ">")
	if open < 0 || end < open {
		return "", false
	}
	prefix, tail := family[:open], family[end+1:]
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, tail) || len(key) <= len(prefix)+len(tail) {
		return "", false
	}
	member := key[len(prefix) : len(key)-len(tail)]
	return member, !strings.Contains(member, ".")
}

// familyKeys lists the keys of a family: one per member, or the template name when any name is allowed.
func familyKeys(s KeySpec) []string {
	if s.Members == nil {
		return []string{s.Name}
	}
	open, end := strings.Index(s.Name, "<"), strings.Index(s.Name, ">")
	var keys []string
	for member := range s.Members {
		keys = append(keys, s.Name[:open]+member+s.Name[end+1:])
	}
	sort.Strings(keys)
	return keys
}

// allKeyNames lists every settable key name for closest-match suggestions.
func (m *Manager) allKeyNames() []string {
	names := make([]string, 0, len(m.specs)+len(roleNames)*len(agentFieldNames))
	for _, s := range m.specs {
		if s.Family {
			names = append(names, familyKeys(s)...)
		} else {
			names = append(names, s.Name)
		}
	}
	for _, r := range roleNames {
		for _, f := range agentFieldNames {
			names = append(names, "agents."+r+"."+f)
		}
	}
	sort.Strings(names)
	return names
}

// unknownKey builds the error for a key outside the table, naming the closest key.
func (m *Manager) unknownKey(key string) UnknownKeyError {
	return UnknownKeyError{Key: key, Closest: closest(key, m.allKeyNames())}
}

// closest returns the name nearest to key by edit distance; ties go to the first in order.
func closest(key string, names []string) string {
	best, bestD := "", -1
	for _, n := range names {
		if d := editDistance(key, n); bestD < 0 || d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

// editDistance counts the single-character edits between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
