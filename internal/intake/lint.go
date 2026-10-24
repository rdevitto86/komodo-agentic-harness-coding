package intake

import (
	"fmt"
	"os"
	"path/filepath"
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

// RuleFailFirst names the finding of a done_when command that already passes on the base commit. Plan raises it, never Lint.
const RuleFailFirst Rule = "fail_first"

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

// pointScale holds the allowed story points.
var pointScale = []int{1, 2, 3, 5, 8}

// The three acceptance shapes.
var (
	earsWhen  = regexp.MustCompile(`^WHEN \S.* THE SYSTEM SHALL \S.*$`)
	earsIf    = regexp.MustCompile(`^IF \S.* THEN THE SYSTEM SHALL \S.*$`)
	earsWhile = regexp.MustCompile(`^WHILE \S.* THE SYSTEM SHALL \S.*$`)
	hasShall  = regexp.MustCompile(`\bSHALL\b`)
)

// Lint runs the eight checks on a stored plan and returns their findings. tracked is every tracked file of the base commit;
// it feeds the case-collision and overlapping-glob checks. A stored plan must carry its plan id.
func Lint(b Blueprint, protected, tracked []string) []Finding {
	return lintPlan(b, true, protected, tracked)
}

// LintDraft parses a planner draft in memory and runs the same checks as Lint. A draft of a new plan leaves the plan id out,
// and a draft that amends a plan carries the id of existing; any other id is a grammar finding. A new group must take the
// next number after the groups of existing.
func LintDraft(d PlanDraft, existing Blueprint, protected, tracked []string) []Finding {
	i := slices.IndexFunc(d.Files, func(f DraftFile) bool { return f.Path == "plan.md" })
	if i < 0 {
		return []Finding{grammar("plan.md", "draft has no plan.md")}
	}

	b, found := Parse(d.Files[i].Content)
	if b.Plan != "" && b.Plan != existing.Plan {
		found = append(found, grammar(
			"plan.md",
			fmt.Sprintf("id %s is not the id of the plan being amended; a new plan leaves id out", b.Plan),
		))
	}

	found = append(found, numberFindings(existing, b)...)

	return mergeFindings(found, lintPlan(b, false, protected, tracked))
}

// Approve reads plan.md of a stored plan, runs Lint on it, rewrites blueprint.json from it with each group's card hash and
// returns the blueprint. A failing finding refuses the plan: the zero blueprint comes back and blueprint.json stays as it was.
func Approve(home, repo, plan string, protected, tracked []string) (Blueprint, []Finding, error) {
	if err := checkPlanName(repo, plan); err != nil {
		return Blueprint{}, nil, err
	}

	data, err := os.ReadFile(filepath.Join(PlanDir(home, repo, plan), "plan.md"))
	if err != nil {
		return Blueprint{}, nil, fmt.Errorf("read the plan.md of %s: %w", plan, err)
	}

	text := string(data)
	b, found := Parse(text)
	b.Repo = repo

	if b.Plan != "" && b.Plan != plan {
		found = append(found, grammar("plan.md", fmt.Sprintf("id %s is not the plan folder %s", b.Plan, plan)))
	}

	found = mergeFindings(found, Lint(b, protected, tracked))
	if len(failing(found)) > 0 {
		return Blueprint{}, found, nil
	}

	for i, g := range b.Groups {
		if b.Groups[i].Hash, err = CardHash(text, g); err != nil {
			return Blueprint{}, nil, fmt.Errorf("hash group %s: %w", g.Name, err)
		}
	}

	if err := WriteBlueprint(home, b); err != nil {
		return Blueprint{}, nil, err
	}

	return b, found, nil
}

// OverEstimate warns when a task's real changed lines exceed twice its estimate.
func OverEstimate(t PlanTask, actualLines int) (Finding, bool) {
	const overEstimateFold = 2
	if actualLines <= overEstimateFold*t.EstimateLines {
		return Finding{}, false
	}

	return Finding{
		Rule:     RuleSizing,
		Severity: SeverityWarn,
		Path:     "plan.md",
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

// mergeFindings joins finding lists, drops a finding that repeats an earlier one and sorts the rest.
func mergeFindings(lists ...[]Finding) []Finding {
	seen := map[Finding]bool{}
	out := []Finding{}

	for _, l := range lists {
		for _, f := range l {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}

	return sortFindings(out)
}

// lintPlan runs every check on a plan. A stored plan must carry its plan id; a draft need not.
func lintPlan(b Blueprint, stored bool, protected, tracked []string) []Finding {
	var out []scoped
	add := func(f Finding, groups ...string) { out = append(out, scoped{f, groups}) }

	if stored && b.Plan == "" {
		add(grammar("plan.md", "id is missing"))
	}

	if b.Release == "" {
		add(required("plan.md", "release is missing or empty"))
	}

	for _, g := range b.Groups {
		lintGroup(g, protected, add)
	}

	nodes := planNodes(b)
	graph, gf := buildGraph(nodes, tracked)
	out = append(out, gf...)
	if graph.cycle() == nil {
		waveIsolation(nodes, graph, add)
	}

	caseCollisions(nodes, tracked, add)
	out = append(out, duplicateGroupIDs(b)...)

	return sortFindings(strip(out))
}

// lintGroup runs the checks that look at one group alone: required, sizing, ears and protected.
func lintGroup(g PlanGroup, protected []string, add func(Finding, ...string)) {
	// The fixed sizing limits.
	const (
		maxTaskFiles  = 5
		maxTaskLines  = 300
		maxGroupTasks = 8
	)

	gp := "plan.md#" + g.Name

	fail := func(r Rule, path, msg string, a ...any) {
		add(Finding{Rule: r, Severity: SeverityFail, Path: path, Message: fmt.Sprintf(msg, a...)}, g.Name)
	}
	if !slices.Contains(pointScale, g.Points) {
		fail(RuleSizing, gp, "points %d is not on the scale 1, 2, 3, 5, 8", g.Points)
	}
	if len(g.Tasks) > maxGroupTasks {
		fail(RuleSizing, gp, "group has %d tasks; at most %d", len(g.Tasks), maxGroupTasks)
	}
	if len(g.Files) == 0 {
		fail(RuleRequired, gp, "files is missing or empty")
	}

	for _, f := range g.Files {
		if matchesAny(protected, f) {
			fail(RuleProtected, gp, "file %s is a protected path", f)
		}
	}

	folded := map[string]string{}

	for _, t := range g.Tasks {
		tp := gp + "/" + t.Name
		if prev, ok := folded[strings.ToLower(t.Name)]; ok {
			fail(RuleCaseCollision, tp, "task %s differs only by case from %s", t.Name, prev)
		}

		folded[strings.ToLower(t.Name)] = t.Name
		if len(t.Files) == 0 {
			fail(RuleRequired, tp, "files is missing or empty")
		}
		if len(t.Files) > maxTaskFiles {
			fail(RuleSizing, tp, "task names %d files; at most %d", len(t.Files), maxTaskFiles)
		}
		if t.EstimateLines > maxTaskLines {
			fail(RuleSizing, tp, "task estimates %d lines; at most %d", t.EstimateLines, maxTaskLines)
		}
		if t.DoneWhen == "" && len(t.Acceptance) == 0 {
			fail(RuleRequired, tp, "task has neither a done_when nor an acceptance criterion")
		}

		for _, f := range t.Files {
			if !slices.Contains(g.Files, f) {
				fail(RuleRequired, tp, "file %s is not in the files of %s", f, g.Name)
			}
		}

		for i, a := range t.Acceptance {
			if msg := earsProblem(a); msg != "" {
				fail(RuleEARS, tp, "acceptance item %d %s", i+1, msg)
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

// waveIsolation fails two groups of one wave that list the same file.
func waveIsolation(nodes []node, graph Graph, add func(Finding, ...string)) {
	paths := map[string]string{}
	files := map[string][]string{}
	for _, n := range nodes {
		paths[n.name] = n.path
		files[n.name] = n.files
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
func caseCollisions(nodes []node, tracked []string, add func(Finding, ...string)) {
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
	for _, n := range nodes {
		paths[n.name] = n.path
		order = append(order, n.name)
		for _, f := range n.files {
			note(f, n.name)
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

// numberFindings fails a draft group that is new but does not take the next number after the groups of existing.
func numberFindings(existing, draft Blueprint) []Finding {
	var found []Finding

	have := map[string]bool{}
	next := 1

	for _, g := range existing.Groups {
		have[g.Name] = true
		next = max(next, groupNumber(g.Name)+1)
	}

	for _, g := range draft.Groups {
		if have[g.Name] {
			continue
		}

		want := fmt.Sprintf("tg-%d", next)
		if g.Name != want {
			found = append(found, grammar(
				"plan.md#"+g.Name,
				fmt.Sprintf("new group must be %s, the next number of the plan", want),
			))
		}

		next++
	}

	return found
}

// groupNumber returns N of a group label tg-N, or 0.
func groupNumber(name string) int {
	var n int
	if _, err := fmt.Sscanf(name, "tg-%d", &n); err != nil {
		return 0
	}

	return n
}

// duplicateGroupIDs reports each group whose id repeats an earlier group's id; the finding names both groups.
func duplicateGroupIDs(b Blueprint) []scoped {
	var found []scoped

	first := map[string]string{}

	for _, g := range b.Groups {
		if g.ID == "" {
			continue
		}

		if prev, ok := first[g.ID]; ok {
			found = append(found, scoped{
				grammar("plan.md#"+g.Name, fmt.Sprintf("id %s is already the id of %s", g.ID, prev)),
				[]string{g.Name, prev},
			})
			continue
		}

		first[g.ID] = g.Name
	}

	return found
}
