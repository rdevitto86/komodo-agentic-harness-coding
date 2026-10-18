package intake

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Severity says whether a finding stops the plan or only warns.
type Severity string

// The two severities of a finding.
const (
	SeverityFail Severity = "fail"
	SeverityWarn Severity = "warn"
)

// Rule names the lint check that produced a finding.
type Rule string

// The eight lint checks.
const (
	RuleGrammar       Rule = "grammar"
	RuleRequired      Rule = "required"
	RuleWaveIsolation Rule = "wave_isolation"
	RuleProtected     Rule = "protected"
	RuleSizing        Rule = "sizing"
	RuleGraph         Rule = "graph"
	RuleEARS          Rule = "ears"
	RuleCaseCollision Rule = "case_collision"
)

// Finding is one failed or warned check, naming the rule and the file.
type Finding struct {
	Rule     Rule     `json:"rule"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`    // the file the rule failed on
	Message  string   `json:"message"` // what is wrong, in one sentence
}

// scoped is a finding with the names of the groups it concerns, so a draft check can keep only its own.
type scoped struct {
	Finding
	groups []string
}

const backlogDir = "docs/backlog"

// pointScale holds the allowed story points.
var pointScale = []int{1, 2, 3, 5, 8}

// The three acceptance shapes.
var (
	earsWhen  = regexp.MustCompile(`^WHEN \S.* THE SYSTEM SHALL \S.*$`)
	earsIf    = regexp.MustCompile(`^IF \S.* THEN THE SYSTEM SHALL \S.*$`)
	earsWhile = regexp.MustCompile(`^WHILE \S.* THE SYSTEM SHALL \S.*$`)
	hasShall  = regexp.MustCompile(`\bSHALL\b`)
)

// Lint runs the eight checks on the loaded task lists and returns their findings. tracked is every tracked file of the base
// commit; it feeds the case-collision and overlapping-glob checks.
func Lint(epics []Epic, protected, tracked []string) []Finding {
	return sortFindings(strip(lintAll(epics, protected, tracked)))
}

// LintDraft parses a draft in memory and runs the same checks as Lint. existing holds the stored epics: a draft group
// replaces the stored group of the same name, and only findings about draft
// groups come back.
func LintDraft(d PlanDraft, existing []Epic, protected, tracked []string) []Finding {
	if len(d.Files) == 0 {
		return []Finding{grammar("", "draft has no files")}
	}

	epics, found := parseFiles(d.Files)
	drafted := map[string]bool{}
	for _, e := range epics {
		for _, g := range e.Groups {
			drafted[g.Name] = true
		}
	}

	merged := mergeEpics(existing, epics)
	found = append(found, numberFindings(existing, epics)...)

	all := append(lintAll(merged, protected, tracked), duplicateIDs(merged)...)
	for _, f := range all {
		if slices.ContainsFunc(f.groups, func(n string) bool { return drafted[n] }) {
			found = append(found, f.Finding)
		}
	}

	return sortFindings(found)
}

// OverEstimate warns when a task's real changed lines exceed twice its estimate.
func OverEstimate(t Task, actualLines int) (Finding, bool) {
	const overEstimateFold = 2
	if actualLines <= overEstimateFold*t.EstimateLines {
		return Finding{}, false
	}

	return Finding{
		Rule:     RuleSizing,
		Severity: SeverityWarn,
		Path:     t.Name + ".md",
		Message: fmt.Sprintf(
			"task %s changed %d lines against an estimate of %d",
			t.Name, actualLines, t.EstimateLines,
		),
	}, true
}

// strip drops the group names from scoped findings.
func strip(in []scoped) []Finding {
	out := make([]Finding, len(in))
	for i, s := range in {
		out[i] = s.Finding
	}
	return out
}

// lintAll runs every check and returns the findings with the groups each concerns.
func lintAll(epics []Epic, protected, tracked []string) []scoped {
	var out []scoped
	add := func(f Finding, groups ...string) { out = append(out, scoped{f, groups}) }
	for _, e := range epics {
		for _, g := range e.Groups {
			lintGroup(e.Number, g, protected, add)
		}
	}

	graph, gf := buildGraph(epics, tracked)
	out = append(out, gf...)
	if graph.cycle() == nil {
		waveIsolation(epics, graph, add)
	}

	caseCollisions(epics, tracked, add)
	return out
}

// lintGroup runs the checks that look at one group alone: required, sizing, ears and protected.
func lintGroup(epic int, g Group, protected []string, add func(Finding, ...string)) {
	// The fixed sizing limits.
	const (
		maxTaskFiles  = 5
		maxTaskLines  = 300
		maxGroupTasks = 8
	)

	tg := groupPath(epic, g.Name)

	fail := func(r Rule, path, msg string, a ...any) {
		add(Finding{Rule: r, Severity: SeverityFail, Path: path, Message: fmt.Sprintf(msg, a...)}, g.Name)
	}
	if !slices.Contains(pointScale, g.Points) {
		fail(RuleSizing, tg, "points %d is not on the scale 1, 2, 3, 5, 8", g.Points)
	}
	if len(g.Tasks) > maxGroupTasks {
		fail(RuleSizing, tg, "group has %d tasks; at most %d", len(g.Tasks), maxGroupTasks)
	}
	if len(g.Acceptance) == 0 {
		fail(RuleRequired, tg, "group has no acceptance item")
	}

	for i, a := range g.Acceptance {
		if msg := earsProblem(a); msg != "" {
			fail(RuleEARS, tg, "acceptance item %d %s", i+1, msg)
		}
	}

	for _, f := range g.Files {
		if protectedPath(f, protected) {
			fail(RuleProtected, tg, "file %s is a protected path or a task list", f)
		}
	}

	folded := map[string]string{}

	for _, t := range g.Tasks {
		tp := fmt.Sprintf("epic-%d/%s/%s.md", epic, g.Name, t.Name)
		if prev, ok := folded[strings.ToLower(t.Name)]; ok {
			fail(RuleCaseCollision, tp, "task file %s.md differs only by case from %s.md", t.Name, prev)
		}

		folded[strings.ToLower(t.Name)] = t.Name
		if len(t.Files) > maxTaskFiles {
			fail(RuleSizing, tp, "task names %d files; at most %d", len(t.Files), maxTaskFiles)
		}
		if t.EstimateLines > maxTaskLines {
			fail(RuleSizing, tp, "task estimates %d lines; at most %d", t.EstimateLines, maxTaskLines)
		}

		for _, f := range t.Files {
			if !slices.Contains(g.Files, f) {
				fail(RuleRequired, tp, "file %s is not in the files of %s", f, g.Name)
			}
		}
	}
}

// earsProblem returns what is wrong with an acceptance item's shape, or "" when it matches one of the three.
func earsProblem(a Acceptance) string {
	text := strings.TrimSpace(a.Keyword + " " + a.Text)
	if !hasShall.MatchString(text) {
		return "has no SHALL"
	}
	if earsWhen.MatchString(text) || earsIf.MatchString(text) || earsWhile.MatchString(text) {
		return ""
	}

	return "is not WHEN <event> THE SYSTEM SHALL <response>, " +
		"IF <condition> THEN THE SYSTEM SHALL <response> " +
		"or WHILE <state> THE SYSTEM SHALL <response>"
}

// protectedPath reports whether a file is under docs/backlog or matches a protected pattern.
func protectedPath(f string, protected []string) bool {
	return f == backlogDir || strings.HasPrefix(f, backlogDir+"/") || matchesAny(protected, f)
}

// waveIsolation fails two groups of one wave that list the same file.
func waveIsolation(epics []Epic, graph Graph, add func(Finding, ...string)) {
	paths := map[string]string{}
	files := map[string][]string{}
	for _, e := range epics {
		for _, g := range e.Groups {
			paths[g.Name] = groupPath(e.Number, g.Name)
			files[g.Name] = g.Files
		}
	}

	for _, wave := range graph.Waves() {
		for i, a := range wave {
			for _, b := range wave[i+1:] {
				var shared []string
				for _, f := range files[b] {
					if slices.Contains(files[a], f) && !slices.Contains(shared, f) {
						shared = append(shared, f)
					}
				}

				if len(shared) > 0 {
					slices.Sort(shared)
					add(Finding{
						Rule:     RuleWaveIsolation,
						Severity: SeverityFail,
						Path:     paths[b],
						Message: fmt.Sprintf(
							"%s and %s run in one wave and both list %s",
							b, a, strings.Join(shared, ", "),
						),
					}, a, b)
				}
			}
		}
	}
}

// caseCollisions fails paths that differ only by case when at least one comes from a group's files.
func caseCollisions(epics []Epic, tracked []string, add func(Finding, ...string)) {
	type owner struct{ path, group string }

	byFold := map[string][]owner{}
	seen := map[string]bool{}
	note := func(p, group string) {
		k := group + "\x00" + p
		if seen[k] {
			return
		}
		seen[k] = true
		f := strings.ToLower(p)
		byFold[f] = append(byFold[f], owner{p, group})
	}
	for _, p := range tracked {
		note(p, "")
	}

	var order []string

	paths := map[string]string{}
	for _, e := range epics {
		for _, g := range e.Groups {
			paths[g.Name] = groupPath(e.Number, g.Name)
			order = append(order, g.Name)
			for _, f := range g.Files {
				note(f, g.Name)
			}
		}
	}

	keys := make([]string, 0, len(byFold))
	for k := range byFold {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		distinct := map[string]bool{}

		var groups []string
		for _, o := range byFold[k] {
			distinct[o.path] = true
			if o.group != "" && !slices.Contains(groups, o.group) {
				groups = append(groups, o.group)
			}
		}
		if len(distinct) < 2 || len(groups) == 0 {
			continue
		}

		names := make([]string, 0, len(distinct))
		for p := range distinct {
			names = append(names, p)
		}

		slices.Sort(names)

		first := slices.IndexFunc(order, func(n string) bool { return slices.Contains(groups, n) })
		add(Finding{Rule: RuleCaseCollision, Severity: SeverityFail, Path: paths[order[first]],
			Message: "paths differ only by case: " + strings.Join(names, ", ")}, groups...)
	}
}

// mergeEpics overlays the draft's groups on the stored epics; a draft group replaces a stored group of the same name.
func mergeEpics(existing, draft []Epic) []Epic {
	byNum := map[int]*Epic{}

	var nums []int

	for _, src := range [][]Epic{existing, draft} {
		for _, e := range src {
			cur := byNum[e.Number]
			if cur == nil {
				cur = &Epic{Number: e.Number}
				byNum[e.Number] = cur
				nums = append(nums, e.Number)
			}

			for _, g := range e.Groups {
				if i := slices.IndexFunc(
					cur.Groups,
					func(x Group) bool { return x.Name == g.Name },
				); i >= 0 {
					cur.Groups[i] = g
				} else {
					cur.Groups = append(cur.Groups, g)
				}
			}
		}
	}

	slices.Sort(nums)

	out := make([]Epic, 0, len(nums))
	for _, n := range nums {
		e := byNum[n]
		sort.SliceStable(e.Groups, func(i, j int) bool {
			return groupIndex(e.Groups[i].Name) < groupIndex(e.Groups[j].Name)
		})
		out = append(out, *e)
	}

	return out
}

// groupIndex returns M of a group name tg-N.M, or 0.
func groupIndex(name string) int {
	var n, m int
	if _, err := fmt.Sscanf(name, "tg-%d.%d", &n, &m); err != nil {
		return 0
	}
	return m
}

// sortFindings orders findings by path and then rule, keeping the order of equal ones.
func sortFindings(f []Finding) []Finding {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Path != f[j].Path {
			return f[i].Path < f[j].Path
		}
		return f[i].Rule < f[j].Rule
	})
	return f
}

// numberFindings fails a draft group that is new but does not take the next number of its epic, and a draft group whose
// status is not todo.
func numberFindings(existing, draft []Epic) []Finding {
	var found []Finding

	for _, e := range draft {
		next := 1
		have := map[string]bool{}
		for _, se := range existing {
			if se.Number != e.Number {
				continue
			}
			for _, g := range se.Groups {
				have[g.Name] = true
				next = max(next, groupIndex(g.Name)+1)
			}
		}

		for _, g := range e.Groups {
			if g.Status != "todo" {
				found = append(found, grammar(
					groupPath(e.Number, g.Name),
					fmt.Sprintf("status %q: a plan writes status todo", g.Status),
				))
			}
			if have[g.Name] {
				continue
			}

			want := fmt.Sprintf("tg-%d.%d", e.Number, next)
			if g.Name != want {
				found = append(found, grammar(
					groupPath(e.Number, g.Name),
					fmt.Sprintf("new group must be %s, the next number of epic-%d", want, e.Number),
				))
			}

			next++
		}
	}

	return found
}
