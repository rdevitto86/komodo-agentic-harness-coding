package contextmgr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// decodeStrict decodes one JSON value into v and refuses any field v does not declare.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// loadIndex reads and checks embed/skills/index.json.
func loadIndex(fsys fs.FS) (Index, error) {
	raw, err := fs.ReadFile(fsys, "embed/skills/index.json")
	if err != nil {
		return Index{}, fmt.Errorf("read skill index: %w", err)
	}
	var idx Index
	if err := decodeStrict(raw, &idx); err != nil {
		return Index{}, fmt.Errorf("decode skill index: %w", err)
	}
	if idx.V != 1 {
		return Index{}, fmt.Errorf("skill index version %d is not 1", idx.V)
	}
	for i, e := range idx.Skills {
		if len(e.SHA256) != 64 || e.Name == "" || (i > 0 && idx.Skills[i-1].Name >= e.Name) {
			return Index{}, fmt.Errorf("skill index row %d (%q) is malformed or out of order", i, e.Name)
		}
	}
	return idx, nil
}

// loadTemplates reads and parses the 4 shipped templates.
func loadTemplates(fsys fs.FS) (map[string]Template, error) {
	out := map[string]Template{}
	for _, a := range Agents {
		raw, err := fs.ReadFile(fsys, "embed/templates/"+a+".md")
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", a, err)
		}
		tpl, err := ParseTemplate(a, string(raw))
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", a, err)
		}
		out[a] = tpl
	}
	return out, nil
}

// refuseLocal checks one local SKILL.md and returns its row, or the reason it is refused.
func refuseLocal(folder string, raw []byte, shipped map[SkillName]bool, phrases []string, count Counter) (IndexEntry, error) {
	text := string(raw)
	fm, err := parseSkill(text)
	switch {
	case err != nil:
		return IndexEntry{}, err
	case string(fm.Name) != folder:
		return IndexEntry{}, fmt.Errorf("name %q differs from the folder name", fm.Name)
	case shipped[fm.Name]:
		return IndexEntry{}, fmt.Errorf("name %q is a shipped skill", fm.Name)
	}
	if err := checkSkillSize(count, text); err != nil {
		return IndexEntry{}, err
	}
	if phrase, hit := findPhrase(text, phrases); hit {
		return IndexEntry{}, fmt.Errorf("steering phrase %q", phrase)
	}
	return IndexEntry{Name: fm.Name, Description: fm.Description, Globs: fm.Globs, Markers: fm.Markers, Sections: fm.Sections, SHA256: fingerprint(raw)}, nil
}

// loadLocalSkills reads skills/*/SKILL.md under the home folder, in folder order; each loads disabled.
func loadLocalSkills(home string, shipped map[SkillName]bool, phrases []string, count Counter) ([]*item, []Warning) {
	if home == "" {
		return nil, nil
	}
	dir := filepath.Join(home, "skills")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []Warning{{Code: "local_skills_unreadable", Path: dir, Note: err.Error()}}
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	var items []*item
	var warns []Warning
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		file := filepath.Join(dir, e.Name(), "SKILL.md")
		raw, err := os.ReadFile(file)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				warns = append(warns, Warning{Code: "local_skill_refused", Path: file, Note: err.Error()})
			}
			continue
		}
		entry, err := refuseLocal(e.Name(), raw, shipped, phrases, count)
		if err != nil {
			warns = append(warns, Warning{Code: "local_skill_refused", Path: file, Note: err.Error()})
			continue
		}
		items = append(items, &item{state: ItemState{Name: entry.Name, Origin: OriginLocal, Allow: []string{}, Always: []string{}}, entry: entry, path: file})
	}
	return items, warns
}

// loadConnections reads connections.json under the home folder; each entry loads disabled.
// An unreadable file or an unknown key loads no entry and returns a warning.
func loadConnections(home string) ([]Connection, []Warning) {
	if home == "" {
		return nil, nil
	}
	file := filepath.Join(home, "connections.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []Warning{{Code: "connections_unreadable", Path: file, Note: err.Error()}}
	}
	var cf ConnectionFile
	if err := decodeStrict(raw, &cf); err != nil || cf.V != 1 {
		note := "version is not 1"
		if err != nil {
			note = err.Error()
		}
		return nil, []Warning{{Code: "connections_unreadable", Path: file, Note: note}}
	}
	var out []Connection
	var warns []Warning
	seen := map[string]bool{}
	for _, c := range cf.Connections {
		if reason := refuseConnection(c, seen); reason != "" {
			warns = append(warns, Warning{Code: "connection_refused", Path: file, Note: fmt.Sprintf("connection %q: %s", c.Name, reason)})
			continue
		}
		seen[c.Name] = true
		c.Enabled, c.Origin = false, OriginLocal
		c.Args, c.Env, c.Allow, c.Hosts = nonNil(c.Args), nonNil(c.Env), nonNil(c.Allow), nonNil(c.Hosts)
		out = append(out, c)
	}
	return out, warns
}

// refuseConnection returns why an entry cannot load, or an empty string.
func refuseConnection(c Connection, seen map[string]bool) string {
	switch {
	case c.Name == "" || strings.ContainsAny(c.Name, " \t\n"):
		return "name is empty or holds white space"
	case c.Name == komodoConnection:
		return "the name komodo is reserved"
	case seen[c.Name]:
		return "name is repeated"
	case c.Command == "":
		return "command is empty"
	}
	if bad := badAgent(c.Allow); bad != "" {
		return fmt.Sprintf("allow names %q, which is not an agent", bad)
	}
	return ""
}

// nonNil returns s, or an empty slice when s is nil.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// badAgent returns the first name that is not one of the 4 agents, or an empty string.
func badAgent(names []string) string {
	for _, n := range names {
		if !validAgent(n) {
			return n
		}
	}
	return ""
}
