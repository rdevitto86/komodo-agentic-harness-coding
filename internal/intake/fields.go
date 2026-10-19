package intake

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// block is a parsed field block: the values by key and the lines after it.
type block struct {
	vals map[string][]string
	body []numbered
}

// has reports whether the key appeared in the block.
func (b block) has(k string) bool {
	_, ok := b.vals[k]
	return ok
}

// one returns the key's single value, or "" when the key is absent or empty.
func (b block) one(k string) string {
	if v := b.vals[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// numbered is a body line with its 1-based line number.
type numbered struct {
	n    int
	text string
}

// uuid7 matches a version 7 universally unique identifier in text form.
var uuid7 = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// fieldLine matches a bare key: value line of a field block.
var fieldLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):[ \t]*(.*)$`)

// The keys of the two file kinds; the value says whether the key holds a list.
var (
	tgKeys = map[string]bool{
		"id": false, "goal": false, "subsystem": false, "touches": true, "priority": false,
		"points": false, "status": false, "files": true, "dependencies": true,
	}
	taskKeys = map[string]bool{"files": true, "done_when": false, "estimate_lines": false}
)

// validStatus holds the five group statuses.
var validStatus = map[string]bool{
	"todo":             true,
	"in_progress":      true,
	"blocked":          true,
	"needs_refinement": true,
	"done":             true,
}

// parseTG reads a TG.md into g; every problem is a finding and the fields that read well still fill g. A draft may leave
// id out, because Komodo assigns it when the plan is saved.
func parseTG(path, text string, g *Group, idOptional bool) []Finding {
	b, found := parseBlock(path, text, tgKeys)
	fail := func(msg string, a ...any) {
		found = append(found, grammar(path, fmt.Sprintf(msg, a...)))
	}
	if id := b.one("id"); id == "" {
		if !idOptional {
			fail("id is missing")
		}
	} else if !uuid7.MatchString(id) {
		fail("id %q is not a version 7 identifier", id)
	} else {
		g.ID = id
	}
	if g.Goal = b.one("goal"); g.Goal == "" {
		fail("goal is missing")
	}
	if v := b.one("subsystem"); v != "" {
		g.Subsystem = v
	}

	g.Touches = b.vals["touches"]
	if v := b.one("priority"); v != "" {
		if p := Priority(v); p == PriorityHigh || p == PriorityNormal {
			g.Priority = p
		} else {
			fail("priority %q is not high or normal", v)
		}
	}
	if v := b.one("points"); v == "" {
		fail("points is missing")
	} else if n, err := strconv.Atoi(v); err != nil {
		fail("points %q is not a number", v)
	} else {
		g.Points = n
	}
	if v := b.one("status"); v != "" {
		if validStatus[v] {
			g.Status = v
		} else {
			fail("status %q is not todo, in_progress, blocked, needs_refinement or done", v)
		}
	}

	g.Files = b.vals["files"]

	g.Dependencies = b.vals["dependencies"]
	if !b.has("files") || len(g.Files) == 0 {
		found = append(found, required(path, "files is missing or empty"))
	}
	if !b.has("dependencies") {
		found = append(found, required(
			path,
			"dependencies is missing; write the key with no value when the group waits on none",
		))
	}

	items, fs := parseAcceptance(path, b.body)
	g.Acceptance = items
	return append(found, fs...)
}

// parseBlock reads the bare key: value lines at the top of a file, ending at the first blank line. A key outside keys, a
// repeated key, an upper-case key, a stray line or a list under a one-value
// key is a grammar finding.
func parseBlock(path, text string, keys map[string]bool) (block, []Finding) {
	b := block{vals: map[string][]string{}}

	var found []Finding

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	cur := ""

	i := 0
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if item, ok := strings.CutPrefix(line, "- "); ok {
			if cur == "" {
				found = append(found, grammar(path, fmt.Sprintf("line %d is a list item under no key", i+1)))
			} else if !keys[cur] {
				found = append(found, grammar(path, fmt.Sprintf("%s takes one value, not a list", cur)))
			} else {
				b.vals[cur] = append(b.vals[cur], strings.TrimSpace(item))
			}
			continue
		}

		m := fieldLine.FindStringSubmatch(line)
		if m == nil {
			found = append(found, grammar(path, fmt.Sprintf("line %d is not a key: value line", i+1)))
			cur = ""
			continue
		}

		key, val := m[1], strings.TrimSpace(m[2])
		cur = ""

		switch _, known := keys[key]; {
		case key != strings.ToLower(key):
			found = append(found, grammar(path, fmt.Sprintf("key %s must be lower case", key)))
		case !known:
			found = append(found, grammar(path, fmt.Sprintf("unknown key %s", key)))
		case b.has(key):
			found = append(found, grammar(path, fmt.Sprintf("key %s appears twice", key)))
		default:
			b.vals[key] = []string{}
			if val != "" {
				b.vals[key] = []string{val}
			}
			cur = key
		}
	}

	for ; i < len(lines); i++ {
		b.body = append(b.body, numbered{n: i + 1, text: lines[i]})
	}

	return b, found
}

// required returns a failing required finding.
func required(path, msg string) Finding {
	return Finding{Rule: RuleRequired, Severity: SeverityFail, Path: path, Message: msg}
}

// parseAcceptance reads the "- " items after a TG.md field block, with their indented cmd: and needs: lines.
func parseAcceptance(path string, body []numbered) ([]Acceptance, []Finding) {
	var (
		items []Acceptance
		found []Finding
	)

	for _, l := range body {
		trimmed := strings.TrimSpace(l.text)
		switch {
		case trimmed == "":
		case strings.HasPrefix(l.text, "- "):
			kw, rest, _ := strings.Cut(strings.TrimSpace(l.text[2:]), " ")
			items = append(items, Acceptance{Keyword: kw, Text: strings.TrimSpace(rest)})
		case len(items) > 0 && (l.text[0] == ' ' || l.text[0] == '\t'):
			found = append(found, parseItemLine(path, l, &items[len(items)-1])...)
		default:
			found = append(found, grammar(path, fmt.Sprintf("line %d is not an acceptance item", l.n)))
		}
	}

	return items, found
}

// parseItemLine reads one indented cmd: or needs: line into the acceptance item above it.
func parseItemLine(path string, l numbered, a *Acceptance) []Finding {
	key, val, ok := strings.Cut(strings.TrimSpace(l.text), ":")
	val = strings.TrimSpace(val)

	switch {
	case ok && key == "cmd":
		a.Cmd = val
	case ok && key == "needs":
		var found []Finding

		for _, n := range strings.Split(val, ",") {
			n = strings.TrimSpace(n)
			if n == "network" || n == "docker" || (strings.HasPrefix(n, "service:") && len(n) > len("service:")) {
				a.Needs = append(a.Needs, n)
			} else {
				found = append(found, grammar(
					path,
					fmt.Sprintf("needs value %q is not network, docker or service:<name>", n),
				))
			}
		}

		return found
	default:
		return []Finding{grammar(path, fmt.Sprintf("line %d is not a cmd: or needs: line", l.n))}
	}

	return nil
}

// parseTask reads a tsk-*.md file; its name is the file name without .md.
func parseTask(path, text string) (Task, []Finding) {
	t := Task{Name: strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".md")}
	b, found := parseBlock(path, text, taskKeys)
	t.Files = b.vals["files"]

	t.DoneWhen = b.one("done_when")
	if len(t.Files) == 0 {
		found = append(found, required(path, "files is missing or empty"))
	}
	if t.DoneWhen == "" {
		found = append(found, required(path, "done_when is missing"))
	}
	if v := b.one("estimate_lines"); v == "" {
		found = append(found, grammar(path, "estimate_lines is missing"))
	} else if n, err := strconv.Atoi(v); err != nil {
		found = append(found, grammar(path, fmt.Sprintf("estimate_lines %q is not a number", v)))
	} else {
		t.EstimateLines = n
	}

	var card []string
	for _, l := range b.body {
		card = append(card, l.text)
	}

	t.Card = strings.TrimSpace(strings.Join(card, "\n"))
	return t, found
}
