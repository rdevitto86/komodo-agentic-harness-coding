package intake

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// section is one heading of plan.md and the lines under it, up to the next heading of level 1 to 3.
type section struct {
	level int      // number of # characters
	title string   // heading text
	lines []string // lines under the heading
}

var (
	// headingLine matches a level 1 to 3 Markdown heading.
	headingLine = regexp.MustCompile(`^(#{1,3})[ \t]+(.*?)[ \t]*$`)

	// groupHeading matches the title of a group section.
	groupHeading = regexp.MustCompile(`^tg-[1-9]\d*$`)

	// acceptanceStart matches the first acceptance criterion after a task's card.
	acceptanceStart = regexp.MustCompile(`^- (WHEN|IF|WHILE)\b`)
)

// Parse reads the text of plan.md; grammar problems come back as findings, not errors.
func Parse(text string) (Blueprint, []Finding) {
	var (
		b     Blueprint
		found []Finding
	)

	pre, secs := splitSections(text)
	if blankLines(pre) != "" {
		found = append(found, grammar("plan.md", "text before the # Plan heading"))
	}

	if len(secs) == 0 || secs[0].level != 1 || secs[0].title != "Plan" {
		found = append(found, grammar("plan.md", "the file does not start with a # Plan heading"))
	}

	cur := -1
	seen := map[string]bool{}

	for i, s := range secs {
		switch {
		case s.level == 1 && i == 0 && s.title == "Plan":
			found = append(found, parseHeader(s, &b)...)
		case s.level == 1 && i > 0:
			found = append(found, grammar("plan.md", "second # heading "+strconv.Quote(s.title)))
		case s.level == 2:
			cur = -1
			if !groupHeading.MatchString(s.title) {
				found = append(found, grammar("plan.md", fmt.Sprintf("heading %q is not tg-N", s.title)))
			} else if seen[s.title] {
				found = append(found, grammar("plan.md", fmt.Sprintf("group %s appears twice", s.title)))
			} else {
				seen[s.title] = true
				g, gf := parseGroupSection(s)
				b.Groups = append(b.Groups, g)
				found = append(found, gf...)
				cur = len(b.Groups) - 1
			}
		case s.level == 3 && cur < 0:
			found = append(found, grammar("plan.md", fmt.Sprintf("task %s is outside a group", s.title)))
		case s.level == 3:
			found = append(found, addTask(&b.Groups[cur], s)...)
		}
	}

	return b, found
}

// Render writes a blueprint as the text of plan.md; Parse reads the text back to the same blueprint.
func Render(b Blueprint) string {
	var w strings.Builder

	w.WriteString("# Plan\n")
	writeField(&w, "id", b.Plan)
	writeField(&w, "goal", b.Goal)
	writeField(&w, "release", b.Release)

	if len(b.Sources) > 0 {
		writeList(&w, "sources", b.Sources)
	}

	for _, g := range b.Groups {
		w.WriteString("\n## " + g.Name + "\n")
		writeField(&w, "id", g.ID)
		writeField(&w, "goal", g.Goal)
		writeField(&w, "subsystem", g.Subsystem)

		if len(g.Touches) > 0 {
			writeList(&w, "touches", g.Touches)
		}

		writeField(&w, "priority", string(g.Priority))
		fmt.Fprintf(&w, "points: %d\n", g.Points)
		writeList(&w, "files", g.Files)
		writeList(&w, "dependencies", g.Dependencies)

		for _, t := range g.Tasks {
			renderTask(&w, t)
		}
	}

	return w.String()
}

// renderTask writes one ### tsk-* section.
func renderTask(w *strings.Builder, t PlanTask) {
	w.WriteString("\n### " + t.Name + "\n")
	writeList(w, "files", t.Files)
	writeField(w, "done_when", t.DoneWhen)
	fmt.Fprintf(w, "estimate_lines: %d\n", t.EstimateLines)

	if t.Card != "" {
		w.WriteString("\n" + t.Card + "\n")
	}

	if len(t.Acceptance) > 0 {
		w.WriteString("\n")
	}

	for _, a := range t.Acceptance {
		w.WriteString("- " + a.Keyword + " " + a.Text + "\n")

		if a.Cmd != "" {
			w.WriteString("  cmd: " + a.Cmd + "\n")
		}

		if len(a.Needs) > 0 {
			w.WriteString("  needs: " + strings.Join(a.Needs, ", ") + "\n")
		}
	}
}

// writeField writes a key: value line, or nothing when the value is empty.
func writeField(w *strings.Builder, key, val string) {
	if val != "" {
		w.WriteString(key + ": " + val + "\n")
	}
}

// writeList writes a list field: the bare key for none, one line for one value, "- " lines for more.
func writeList(w *strings.Builder, key string, vals []string) {
	switch len(vals) {
	case 0:
		w.WriteString(key + ":\n")
	case 1:
		w.WriteString(key + ": " + vals[0] + "\n")
	default:
		w.WriteString(key + ":\n")

		for _, v := range vals {
			w.WriteString("- " + v + "\n")
		}
	}
}

// splitSections cuts the text at its level 1 to 3 headings, ignoring headings inside code fences; pre holds the lines
// before the first heading.
func splitSections(text string) (pre []string, secs []section) {
	fenced := false

	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		}

		if m := headingLine.FindStringSubmatch(line); m != nil && !fenced {
			secs = append(secs, section{level: len(m[1]), title: m[2]})
			continue
		}

		if len(secs) == 0 {
			pre = append(pre, line)
		} else {
			secs[len(secs)-1].lines = append(secs[len(secs)-1].lines, line)
		}
	}

	return pre, secs
}

// blankLines returns the lines joined and trimmed, which is "" when every line is blank.
func blankLines(lines []string) string { return strings.TrimSpace(strings.Join(lines, "\n")) }

// parseHeader reads the plan header under # Plan into b.
func parseHeader(s section, b *Blueprint) []Finding {
	const path = "plan.md"

	v, found := parseBlock(path, strings.Join(s.lines, "\n"), planKeys)
	if blankLines(bodyLines(v.body)) != "" {
		found = append(found, grammar(path, "text after the field block"))
	}

	b.Plan = v.one("id")
	b.Release = v.one("release")
	b.Sources = v.vals["sources"]

	if b.Goal = v.one("goal"); b.Goal == "" {
		found = append(found, grammar(path, "goal is missing"))
	}

	return found
}

// bodyLines returns the text of numbered lines.
func bodyLines(body []numbered) []string {
	out := make([]string, 0, len(body))
	for _, l := range body {
		out = append(out, l.text)
	}

	return out
}

// parseGroupSection reads the field block of a ## tg-N section.
func parseGroupSection(s section) (PlanGroup, []Finding) {
	path := "plan.md#" + s.title
	g := PlanGroup{Name: s.title, Subsystem: "none", Priority: PriorityNormal}
	v, found := parseBlock(path, strings.Join(s.lines, "\n"), groupKeys)

	fail := func(msg string, a ...any) { found = append(found, grammar(path, fmt.Sprintf(msg, a...))) }
	if blankLines(bodyLines(v.body)) != "" {
		fail("text after the field block")
	}

	if id := v.one("id"); id != "" && !uuid7.MatchString(id) {
		fail("id %q is not a version 7 identifier", id)
	} else {
		g.ID = id
	}

	if g.Goal = v.one("goal"); g.Goal == "" {
		fail("goal is missing")
	}

	if sub := v.one("subsystem"); sub != "" {
		g.Subsystem = sub
	}

	if p := v.one("priority"); p == string(PriorityHigh) || p == string(PriorityNormal) {
		g.Priority = Priority(p)
	} else if p != "" {
		fail("priority %q is not high or normal", p)
	}

	if p := v.one("points"); p == "" {
		fail("points is missing")
	} else if n, err := strconv.Atoi(p); err != nil {
		fail("points %q is not a number", p)
	} else {
		g.Points = n
	}

	g.Touches, g.Files, g.Dependencies = v.vals["touches"], v.vals["files"], v.vals["dependencies"]
	if len(g.Files) == 0 {
		found = append(found, required(path, "files is missing or empty"))
	}

	if !v.has("dependencies") {
		found = append(found, required(
			path,
			"dependencies is missing; write the key with no value when the group waits on none",
		))
	}

	return g, found
}

// addTask parses a ### section and appends it to g.
func addTask(g *PlanGroup, s section) []Finding {
	path := "plan.md#" + g.Name

	if !strings.HasPrefix(s.title, "tsk-") || s.title == "tsk-" {
		return []Finding{grammar(path, fmt.Sprintf("task heading %q does not start with tsk-", s.title))}
	}

	for _, t := range g.Tasks {
		if t.Name == s.title {
			return []Finding{grammar(path, fmt.Sprintf("task %s appears twice", s.title))}
		}
	}

	t, found := parseTaskSection(path+"/"+s.title, s)
	g.Tasks = append(g.Tasks, t)

	return found
}

// parseTaskSection reads a ### tsk-* section: its field block, its card and its acceptance criteria.
func parseTaskSection(path string, s section) (PlanTask, []Finding) {
	t := PlanTask{Name: s.title}
	v, found := parseBlock(path, strings.Join(s.lines, "\n"), taskKeys)
	t.Files = v.vals["files"]
	t.DoneWhen = v.one("done_when")

	cardEnd := len(v.body)

	for i, l := range v.body {
		if acceptanceStart.MatchString(l.text) {
			cardEnd = i
			break
		}
	}

	t.Card = blankLines(bodyLines(v.body[:cardEnd]))

	var af []Finding

	t.Acceptance, af = parseAcceptance(path, v.body[cardEnd:])
	found = append(found, af...)

	if len(t.Files) == 0 {
		found = append(found, required(path, "files is missing or empty"))
	}

	if t.DoneWhen == "" && len(t.Acceptance) == 0 {
		found = append(found, required(path, "task has neither a done_when nor an acceptance criterion"))
	}

	if e := v.one("estimate_lines"); e == "" {
		found = append(found, grammar(path, "estimate_lines is missing"))
	} else if n, err := strconv.Atoi(e); err != nil {
		found = append(found, grammar(path, fmt.Sprintf("estimate_lines %q is not a number", e)))
	} else {
		t.EstimateLines = n
	}

	return t, found
}
