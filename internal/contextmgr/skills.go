// Package contextmgr assembles each agent session's brief from skills, templates, the repo map and
// the repo guidance.
package contextmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
)

// SkillName names one skill, such as "standards-go".
type SkillName string

// Frontmatter is the strict header of a SKILL.md; an unknown key is refused.
type Frontmatter struct {
	V           int       // schema version, 1
	Name        SkillName // equals the skill's folder name
	Description string    // one line shown by List
	Globs       []string  // file patterns that select the skill
	Markers     []string  // repo files that must exist
	Sections    []string  // headings an agent may pull as name#section
}

// Index is the whole of embed/skills/index.json.
type Index struct {
	V      int          `json:"v"`      // schema version, 1
	Skills []IndexEntry `json:"skills"` // one row per shipped skill, sorted by name
}

// IndexEntry is one skill's row in index.json.
type IndexEntry struct {
	Name        SkillName `json:"name"`
	Description string    `json:"description"`
	Globs       []string  `json:"globs"`
	Markers     []string  `json:"markers"`
	Sections    []string  `json:"sections"`
	SHA256      string    `json:"sha256"` // 64 lowercase hex characters, written by the build
	Always      bool      `json:"-"`      // set by List for a skill in the agent's always list
}

// Selected is one skill chosen for a brief, ready to render.
type Selected struct {
	Name   SkillName
	Origin Origin // embedded or local
	SHA8   string // short form of the fingerprint
	Body   string // the SKILL.md text, verified against its fingerprint
}

// Rule is one reviewer rule compiled from a Never or Always line.
type Rule struct {
	ID    string    `json:"id"`    // std.<skill>.<n>
	Skill SkillName `json:"skill"` // the skill the line came from
	Line  string    `json:"line"`  // the source line without its list marker and bold marks
}

// Loader selects and serves skills; every call reads the item table as it stands.
type Loader interface {
	// Select returns the preinject set: always first, then matches by name.
	Select(agent string, files []string, t Tree) ([]Selected, error)
	// Get returns a skill body, or one section for a name#section reference.
	Get(agent, ref string) (string, error)
	// List returns the index rows the agent may reach, with the always ones marked.
	List(agent string) []IndexEntry
}

// The errors of skill selection and pulls.
var (
	ErrSkillUnknown    = errors.New("no skill has that name")
	ErrSkillNotAllowed = errors.New("skill is not available to this agent")
	ErrSectionUnknown  = errors.New("skill has no section with that heading")
	ErrSkillHash       = errors.New("skill body does not match its fingerprint")
)

var _ Loader = (*Manager)(nil)

// frontmatterKeys lists the six keys every header carries, and whether each is a list.
var frontmatterKeys = map[string]bool{
	"v":           false,
	"name":        false,
	"description": false,
	"globs":       true,
	"markers":     true,
	"sections":    true,
}

// Select returns the always skills, then the matching skills by name, each with a verified body.
func (m *Manager) Select(agent string, files []string, t Tree) ([]Selected, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.selectLocked(agent, files, t)
}

// Get returns a verified skill body, or one section for a name#section reference.
func (m *Manager) Get(agent, ref string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	name, section, withSection := strings.Cut(ref, "#")

	it := m.find(SkillName(name))
	if it == nil {
		return "", fmt.Errorf("skill %q: %w", name, ErrSkillUnknown)
	}

	if !it.reaches(agent) {
		return "", fmt.Errorf("skill %q: %w", name, ErrSkillNotAllowed)
	}

	body, err := m.verifiedBody(it)
	if err != nil || !withSection {
		return body, err
	}

	sec, ok := sectionOf(body, section)
	if !ok || !slices.Contains(it.entry.Sections, section) {
		return "", fmt.Errorf(
			"skill %q section %q: %w; sections are %s",
			name,
			section,
			ErrSectionUnknown,
			strings.Join(it.entry.Sections, ", "),
		)
	}

	return sec, nil
}

// List returns the index rows the agent may reach, sorted by name, with its always skills marked.
func (m *Manager) List(agent string) []IndexEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []IndexEntry

	for _, it := range m.items {
		if it.reaches(agent) {
			e := it.entry
			e.Always = slices.Contains(it.state.Always, agent)
			out = append(out, e)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// selectLocked is Select for a caller that already holds the table lock.
func (m *Manager) selectLocked(agent string, files []string, t Tree) ([]Selected, error) {
	if !validAgent(agent) {
		return nil, fmt.Errorf("agent %q: %w", agent, ErrAgentUnknown)
	}

	var picked []*item

	chosen := map[SkillName]bool{}

	for _, it := range m.items {
		if it.reaches(agent) && slices.Contains(it.state.Always, agent) {
			picked = append(picked, it)
			chosen[it.state.Name] = true
		}
	}

	var matched []*item

	for _, it := range m.items {
		if chosen[it.state.Name] || !it.reaches(agent) ||
			!anyGlob(it.entry.Globs, files) || !markersMet(t, it.entry.Markers) {
			continue
		}

		matched = append(matched, it)
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].state.Name < matched[j].state.Name
	})

	out := make([]Selected, 0, len(picked)+len(matched))

	for _, it := range append(picked, matched...) {
		body, err := m.verifiedBody(it)
		if err != nil {
			return nil, err
		}

		out = append(out, Selected{
			Name:   it.state.Name,
			Origin: it.state.Origin,
			SHA8:   it.entry.SHA256[:8],
			Body:   body,
		})
	}

	return out, nil
}

// find returns the table row of a skill, or nil.
func (m *Manager) find(name SkillName) *item {
	for _, it := range m.items {
		if it.state.Name == name {
			return it
		}
	}

	return nil
}

// verifiedBody returns a skill's text from the cache or its file, after checking its fingerprint.
func (m *Manager) verifiedBody(it *item) (string, error) {
	fp, c := it.entry.SHA256, m.deps.Cache

	var key cache.Key

	if c != nil {
		key = c.Key("skill-body", fp)
		if b, ok := c.Get(cache.NSSkills, key); ok {
			if fingerprint(b) != fp {
				return "", fmt.Errorf("skill %s from the cache: %w", it.state.Name, ErrSkillHash)
			}

			return string(b), nil
		}
	}

	var raw []byte

	var err error

	if it.path != "" {
		raw, err = os.ReadFile(it.path)
	} else {
		raw, err = fs.ReadFile(m.fsys, "embed/skills/"+string(it.state.Name)+"/SKILL.md")
	}

	if err != nil {
		return "", fmt.Errorf("read skill %s: %w", it.state.Name, err)
	}

	if fingerprint(raw) != fp {
		return "", fmt.Errorf("skill %s: %w", it.state.Name, ErrSkillHash)
	}

	if c != nil {
		_ = c.Put(cache.NSSkills, key, raw)
	}

	return string(raw), nil
}

// reaches reports whether the item is enabled and the agent is in its allow list.
func (it *item) reaches(agent string) bool {
	return it.state.Enabled && slices.Contains(it.state.Allow, agent)
}

// CompileRules returns the reviewer rules of one standards skill body.
func CompileRules(skill SkillName, body string) []Rule {
	short := strings.TrimPrefix(string(skill), "standards-")

	var out []Rule

	fence := false

	for _, line := range strings.Split(body, "\n") {
		if isFence(line) {
			fence = !fence

			continue
		}

		if fence ||
			(!strings.HasPrefix(line, "- **Never") && !strings.HasPrefix(line, "- **Always")) {
			continue
		}

		text := strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(line, "- "), "**", ""))
		out = append(out, Rule{
			ID:    "std." + short + "." + strconv.Itoa(len(out)+1),
			Skill: skill,
			Line:  text,
		})
	}

	return out
}

// fingerprint returns the SHA-256 of b as 64 lowercase hex characters.
func fingerprint(b []byte) string {
	s := sha256.Sum256(b)

	return hex.EncodeToString(s[:])
}

// isFence reports whether a line opens or closes a fenced code block.
func isFence(line string) bool {
	t := strings.TrimLeft(line, " \t")

	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// splitFrontmatter returns the lines between the opening and closing --- and the text after them.
func splitFrontmatter(text string) ([]string, string, error) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return nil, "", errors.New("file does not open with ---")
	}

	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			return lines[1:i], strings.Join(lines[i+1:], "\n"), nil
		}
	}

	return nil, "", errors.New("frontmatter has no closing ---")
}

// parseScalar reads a plain or double-quoted value.
func parseScalar(raw string) (string, error) {
	if strings.HasPrefix(raw, "\"") {
		s, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("bad quoted value %s", raw)
		}

		return s, nil
	}

	if raw == "" || strings.ContainsAny(raw[:1], "[{&*!|>'%@`") {
		return "", fmt.Errorf("bad value %q", raw)
	}

	return raw, nil
}

// parseFrontmatter reads the header lines: key: value, [] and block lists of "item" lines.
func parseFrontmatter(lines []string) (Frontmatter, error) {
	fm := Frontmatter{Globs: []string{}, Markers: []string{}, Sections: []string{}}
	seen := map[string]bool{}
	list := ""

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		if rest, ok := strings.CutPrefix(line, "  - "); ok {
			if list == "" {
				return fm, fmt.Errorf("list item outside a list: %q", line)
			}

			item, err := parseScalar(strings.TrimSpace(rest))
			if err != nil {
				return fm, err
			}

			switch list {
			case "globs":
				fm.Globs = append(fm.Globs, item)
			case "markers":
				fm.Markers = append(fm.Markers, item)
			default:
				fm.Sections = append(fm.Sections, item)
			}

			continue
		}

		key, val, ok := strings.Cut(line, ":")

		isList, known := frontmatterKeys[key]
		if !ok || !known {
			return fm, fmt.Errorf("unknown or malformed line: %q", line)
		}

		if seen[key] {
			return fm, fmt.Errorf("duplicate key %q", key)
		}

		seen[key] = true
		list = ""
		val = strings.TrimSpace(val)

		switch {
		case isList && val == "":
			list = key
		case isList && val == "[]":
		case isList:
			return fm, fmt.Errorf("key %q takes a list", key)
		default:
			s, err := parseScalar(val)
			if err != nil {
				return fm, fmt.Errorf("key %q: %w", key, err)
			}

			switch key {
			case "v":
				n, err := strconv.Atoi(s)
				if err != nil {
					return fm, fmt.Errorf("key v: %w", err)
				}

				fm.V = n
			case "name":
				fm.Name = SkillName(s)
			default:
				fm.Description = s
			}
		}
	}

	for k := range frontmatterKeys {
		if !seen[k] {
			return fm, fmt.Errorf("missing key %q", k)
		}
	}

	return fm, nil
}

// headingsOf returns the level-2 headings of a body in order, outside code fences.
func headingsOf(body string) []string {
	var out []string

	fence := false

	for _, line := range strings.Split(body, "\n") {
		if isFence(line) {
			fence = !fence

			continue
		}

		if !fence {
			if h, ok := strings.CutPrefix(line, "## "); ok {
				out = append(out, strings.TrimRight(h, " \t\r"))
			}
		}
	}

	return out
}

// parseSkill reads a SKILL.md header and checks its sections against the body's level-2 headings.
func parseSkill(text string) (Frontmatter, error) {
	front, body, err := splitFrontmatter(text)
	if err != nil {
		return Frontmatter{}, err
	}

	fm, err := parseFrontmatter(front)
	if err != nil {
		return fm, err
	}

	switch {
	case fm.V != 1:
		return fm, fmt.Errorf("schema version %d is not 1", fm.V)
	case fm.Name == "":
		return fm, errors.New("name is empty")
	case fm.Description == "" || strings.Contains(fm.Description, "\n") ||
		utf8.RuneCountInString(fm.Description) > 100:
		return fm, errors.New("description must be one line of 1 to 100 characters")
	}

	heads := headingsOf(body)
	seen := map[string]bool{}

	for _, h := range heads {
		if seen[h] {
			return fm, fmt.Errorf("duplicate heading %q", h)
		}

		seen[h] = true

		if !slices.Contains(fm.Sections, h) {
			return fm, fmt.Errorf("heading %q is not in sections", h)
		}
	}

	for _, s := range fm.Sections {
		if !seen[s] {
			return fm, fmt.Errorf("section %q has no heading", s)
		}
	}

	if !slices.Equal(heads, fm.Sections) {
		return fm, errors.New("sections are not in body order")
	}

	return fm, nil
}

// matchGlob reports whether a path matches a pattern; a pattern without a slash matches any path
// segment.
func matchGlob(pattern, p string) bool {
	segs := strings.Split(p, "/")
	if !strings.Contains(pattern, "/") {
		for _, s := range segs {
			if ok, _ := path.Match(pattern, s); ok {
				return true
			}
		}

		return false
	}

	return matchSegments(strings.Split(pattern, "/"), segs)
}

// matchSegments matches pattern segments against path segments, where ** takes any number of
// segments.
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}

	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}

		return false
	}

	if len(segs) == 0 {
		return false
	}

	ok, _ := path.Match(pat[0], segs[0])

	return ok && matchSegments(pat[1:], segs[1:])
}

// markersMet reports whether the tree meets any marker; an empty list is always met.
func markersMet(t Tree, markers []string) bool {
	if len(markers) == 0 {
		return true
	}

	if t == nil {
		return false
	}

	for _, mk := range markers {
		glob, text, withText := strings.Cut(mk, ":")

		for _, p := range t.Paths() {
			if !matchGlob(glob, p) {
				continue
			}

			if !withText {
				return true
			}

			if b, err := t.ReadFile(p); err == nil && strings.Contains(string(b), text) {
				return true
			}
		}
	}

	return false
}

// sectionOf returns the text of one level-2 section, heading line included.
func sectionOf(body, section string) (string, bool) {
	var out []string

	fence, in := false, false

	for _, line := range strings.SplitAfter(body, "\n") {
		bare := strings.TrimRight(line, " \t\r\n")
		if isFence(line) {
			fence = !fence
		} else if !fence {
			if in && (strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")) {
				break
			}

			if !in && strings.HasPrefix(bare, "## ") && strings.TrimPrefix(bare, "## ") == section {
				in = true
			}
		}

		if in {
			out = append(out, line)
		}
	}

	return strings.Join(out, ""), in
}

// checkSkillSize fails a skill body over the token limit.
func checkSkillSize(count Counter, body string) error {
	if n := counterOr(count)(body); n > SkillTokenLimit {
		return fmt.Errorf("skill body is %d tokens, over the limit of %d", n, SkillTokenLimit)
	}

	return nil
}

// anyGlob reports whether some file matches some pattern.
func anyGlob(globs, files []string) bool {
	for _, g := range globs {
		for _, f := range files {
			if matchGlob(g, f) {
				return true
			}
		}
	}

	return false
}
