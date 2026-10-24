package intake

import (
	"fmt"
	"regexp"
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

// The keys of the field blocks; the value says whether the key holds a list.
var (
	taskKeys = map[string]bool{"files": true, "done_when": false, "estimate_lines": false}

	// planKeys are the header keys of plan.md.
	planKeys = map[string]bool{"id": false, "goal": false, "release": false, "sources": true}

	// groupKeys are the keys of a ## tg-N section of plan.md; a group's status is not a field there.
	groupKeys = map[string]bool{
		"id": false, "goal": false, "subsystem": false, "touches": true, "priority": false,
		"points": false, "files": true, "dependencies": true,
	}
)

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

// grammar returns a failing grammar finding.
func grammar(path, msg string) Finding {
	return Finding{Rule: RuleGrammar, Severity: SeverityFail, Path: path, Message: msg}
}

// required returns a failing required finding.
func required(path, msg string) Finding {
	return Finding{Rule: RuleRequired, Severity: SeverityFail, Path: path, Message: msg}
}

// parseAcceptance reads the "- " items after a group's field block, with their indented cmd: and needs: lines.
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
