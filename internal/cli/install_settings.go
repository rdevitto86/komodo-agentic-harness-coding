package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// obj is a JSON object whose keys keep the order they were written in and whose values stay as written.
type obj struct {
	keys []string
	vals map[string]json.RawMessage
}

// newObj returns an empty object.
func newObj() *obj { return &obj{vals: map[string]json.RawMessage{}} }

// parseObj reads a JSON object, keeping key order and the bytes of every value.
func parseObj(raw []byte) (*obj, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}

	o := newObj()

	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		key, _ := kt.(string)
		if _, dup := o.vals[key]; !dup {
			o.keys = append(o.keys, key)
		}
		o.vals[key] = v
	}

	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

// get returns the value of a key.
func (o *obj) get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// set replaces the value of a key, or appends the key.
func (o *obj) set(k string, v []byte) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// del removes a key.
func (o *obj) del(k string) {
	delete(o.vals, k)
	o.keys = slices.DeleteFunc(o.keys, func(s string) bool { return s == k })
}

// bytes returns the compact form, with the keys in their written order.
func (o *obj) bytes() []byte {
	var b bytes.Buffer
	b.WriteByte('{')

	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(quote(k))
		b.WriteByte(':')
		b.WriteString(compact(o.vals[k]))
	}

	b.WriteByte('}')
	return b.Bytes()
}

// quote returns s as a JSON string without escaping & < or >.
func quote(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSpace(b.Bytes())
}

// joinList returns the compact JSON list of the values.
func joinList(l []json.RawMessage) []byte {
	parts := make([]string, len(l))
	for i, v := range l {
		parts[i] = compact(v)
	}
	return []byte("[" + strings.Join(parts, ",") + "]")
}

// compact returns the compact form of a JSON value.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// settingsEntry is one entry install owns in .claude/settings.json.
type settingsEntry struct {
	id    string // the manifest name of the entry
	list  string // the dotted path of the list that holds it
	value string // the entry as compact JSON
}

// settingsEntries are the entries step 6 owns, in write order.
var settingsEntries = []settingsEntry{
	{"permissions.allow Bash(komodo *)", "permissions.allow", `"Bash(komodo *)"`},
	{"permissions.deny Bash(komodo config set*)", "permissions.deny", `"Bash(komodo config set*)"`},
	{"permissions.deny Bash(komodo install*)", "permissions.deny", `"Bash(komodo install*)"`},
	{"permissions.deny Bash(komodo update*)", "permissions.deny", `"Bash(komodo update*)"`},
}

const (
	legacyHookList    = "hooks.PreToolUse"                       // legacyHookList is the list an earlier install wrote a hook to.
	legacyHookCommand = "komodo internal guard"                  // legacyHookCommand is the command of that hook.
	legacyHookEntry   = legacyHookList + " " + legacyHookCommand // legacyHookEntry is its manifest name.
)

// settingsEntryOf returns the owned entry with the manifest name id.
func settingsEntryOf(id string) (settingsEntry, bool) {
	for _, e := range settingsEntries {
		if e.id == id {
			return e, true
		}
	}
	return settingsEntry{}, false
}

// loadSettings reads a settings file; an absent or empty file is an empty object.
func loadSettings(path string) (*obj, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(bytes.TrimSpace(b)) == 0) {
		return newObj(), nil
	}
	if err != nil {
		return nil, err
	}

	o, err := parseObj(b)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", path, err)
	}
	return o, nil
}

// list returns the list at a two-part dotted path; a missing list is empty.
func (o *obj) list(path string) ([]json.RawMessage, error) {
	outer, inner, _ := strings.Cut(path, ".")
	raw, ok := o.get(outer)
	if !ok {
		return nil, nil
	}

	mid, err := parseObj(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not an object: %w", outer, err)
	}

	raw, ok = mid.get(inner)
	if !ok {
		return nil, nil
	}

	var l []json.RawMessage
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("%s is not a list: %w", path, err)
	}
	return l, nil
}

// setList stores a list at a dotted path; an empty list removes the key and any parent left empty.
func (o *obj) setList(path string, l []json.RawMessage) error {
	outer, inner, _ := strings.Cut(path, ".")
	mid := newObj()

	if raw, ok := o.get(outer); ok {
		var err error
		if mid, err = parseObj(raw); err != nil {
			return fmt.Errorf("%s is not an object: %w", outer, err)
		}
	}
	if len(l) == 0 {
		mid.del(inner)
	} else {
		mid.set(inner, joinList(l))
	}
	if len(mid.keys) == 0 {
		o.del(outer)
		return nil
	}

	o.set(outer, mid.bytes())
	return nil
}

// find returns the index of the list element equal to the entry's value, or -1.
func (e settingsEntry) find(l []json.RawMessage) int {
	for i, el := range l {
		if compact(el) == e.value {
			return i
		}
	}
	return -1
}

// hookCommand returns the command of one hook object.
func hookCommand(h json.RawMessage) string {
	var x struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(h, &x)
	return x.Command
}

// findLegacyHook locates the hook an earlier install wrote, inside whichever group of the list now holds it.
func findLegacyHook(l []json.RawMessage) (gi, hi int, group *obj, hooks []json.RawMessage) {
	for gi, el := range l {
		g, err := parseObj(el)
		if err != nil {
			continue
		}

		raw, ok := g.get("hooks")
		if !ok {
			continue
		}

		var hs []json.RawMessage
		if json.Unmarshal(raw, &hs) != nil {
			continue
		}

		for hi, h := range hs {
			if hookCommand(h) == legacyHookCommand {
				return gi, hi, g, hs
			}
		}
	}
	return -1, -1, nil, nil
}

// settingsState reports whether the entry is in the file and the fingerprint of what is there.
func settingsState(path, id string) diskState {
	doc, err := loadSettings(path)
	if err != nil {
		return diskState{}
	}

	if id == legacyHookEntry {
		l, err := doc.list(legacyHookList)
		if err != nil {
			return diskState{}
		}
		if gi, hi, _, hs := findLegacyHook(l); gi >= 0 {
			return diskState{exists: true, hash: sha([]byte(compact(hs[hi])))}
		}
		return diskState{}
	}

	e, ok := settingsEntryOf(id)
	if !ok {
		return diskState{}
	}

	l, err := doc.list(e.list)
	if err != nil {
		return diskState{}
	}
	if i := e.find(l); i >= 0 {
		return diskState{exists: true, hash: sha([]byte(compact(l[i])))}
	}
	return diskState{}
}

// saveSettings writes the document indented, or removes the file when it holds nothing.
func saveSettings(path string, doc *obj) error {
	if len(doc.keys) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = os.Remove(filepath.Dir(path))
		return nil
	}

	var out bytes.Buffer
	if err := json.Indent(&out, doc.bytes(), "", "  "); err != nil {
		return err
	}
	return writeText(path, out.String()+"\n")
}

// putSettings adds the entry to the settings file.
func putSettings(path, id string) error {
	e, ok := settingsEntryOf(id)
	if !ok {
		return fmt.Errorf("unknown settings entry %q", id)
	}

	doc, err := loadSettings(path)
	if err != nil {
		return err
	}

	l, err := doc.list(e.list)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if e.find(l) < 0 {
		l = append(l, json.RawMessage(e.value))
	}

	if err := doc.setList(e.list, l); err != nil {
		return err
	}
	return saveSettings(path, doc)
}

// dropSettings removes the entry from the settings file. The hook an earlier install wrote loses only its command,
// and its group only when no other hook is left.
func dropSettings(path, id string) error {
	if id == legacyHookEntry {
		return dropLegacyHook(path)
	}

	e, ok := settingsEntryOf(id)
	if !ok {
		return fmt.Errorf("unknown settings entry %q", id)
	}

	doc, err := loadSettings(path)
	if err != nil {
		return err
	}

	l, err := doc.list(e.list)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	i := e.find(l)
	if i < 0 {
		return nil
	}

	if err := doc.setList(e.list, slices.Delete(slices.Clone(l), i, i+1)); err != nil {
		return err
	}
	return saveSettings(path, doc)
}

// dropLegacyHook removes every copy of the command an earlier install wrote, and a group only when no other hook is left.
func dropLegacyHook(path string) error {
	doc, err := loadSettings(path)
	if err != nil {
		return err
	}

	l, err := doc.list(legacyHookList)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	for {
		gi, hi, g, hs := findLegacyHook(l)
		if gi < 0 {
			break
		}

		if hs = slices.Delete(slices.Clone(hs), hi, hi+1); len(hs) == 0 {
			l = slices.Delete(slices.Clone(l), gi, gi+1)
		} else {
			g.set("hooks", joinList(hs))
			l[gi] = g.bytes()
		}
	}

	if err := doc.setList(legacyHookList, l); err != nil {
		return err
	}
	return saveSettings(path, doc)
}
