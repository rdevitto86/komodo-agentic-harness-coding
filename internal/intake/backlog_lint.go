package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// --- backlog task lists: lint, graph and card hash over the Epic and Group types

const backlogDir = "docs/backlog"

// lintBacklog runs the eight checks on the loaded task lists and returns their findings.
func lintBacklog(epics []Epic, protected, tracked []string) []Finding {
	return sortFindings(strip(lintAllEpics(epics, protected, tracked)))
}

// lintDraftBacklog parses a draft of task-list files in memory and runs the same checks as lintBacklog. A draft group
// replaces the stored group of the same name, and only findings about draft groups come back.
func lintDraftBacklog(d PlanDraft, existing []Epic, protected, tracked []string) []Finding {
	if len(d.Files) == 0 {
		return []Finding{grammar("", "draft has no files")}
	}

	epics, found := parseFiles(d.Files, true)
	drafted := map[string]bool{}
	for _, e := range epics {
		for _, g := range e.Groups {
			drafted[g.Name] = true
		}
	}

	merged := mergeEpics(existing, epics)
	found = append(found, numberFindingsEpics(existing, epics)...)

	all := append(lintAllEpics(merged, protected, tracked), duplicateIDs(merged)...)
	for _, f := range all {
		if slices.ContainsFunc(f.groups, func(n string) bool { return drafted[n] }) {
			found = append(found, f.Finding)
		}
	}

	return sortFindings(found)
}

// buildGraphEpics builds the dependency graph of the task lists.
func buildGraphEpics(epics []Epic, tracked []string) (Graph, []Finding) {
	g, found := buildGraph(epicNodes(epics), tracked)
	return g, strip(found)
}

// cardHashDir returns the card hash of one group folder: sha256 over TG.md without its status line, then each tsk-*.md in
// byte order of file name.
func cardHashDir(root string, g Group) (string, error) {
	var epic, idx int
	if _, err := fmt.Sscanf(g.Name, "tg-%d.%d", &epic, &idx); err != nil {
		return "", fmt.Errorf("group name %q is not tg-N.M", g.Name)
	}

	rel := path.Join(backlogDir, fmt.Sprintf("epic-%d", epic), g.Name)
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read group folder: %w", err)
	}

	names := []string{"TG.md"}
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "tsk-") && strings.HasSuffix(n, ".md") {
			names = append(names, n)
		}
	}

	sort.Strings(names)

	h := sha256.New()

	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", fmt.Errorf("read card file: %w", err)
		}
		if n == "TG.md" {
			b = dropStatusLine(b)
		}
		h.Write([]byte(path.Join(rel, n) + "\n" + strconv.Itoa(len(b)) + "\n"))
		h.Write(b)
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// dropStatusLine removes the status: line from the field block of a TG.md.
func dropStatusLine(b []byte) []byte {
	var out strings.Builder

	inBlock := true
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if strings.TrimSpace(l) == "" {
			inBlock = false
		}
		if inBlock && strings.HasPrefix(l, "status:") {
			continue
		}
		out.WriteString(l)
	}

	return []byte(out.String())
}

// epicNodes returns every group of the epics as a graph node.
func epicNodes(epics []Epic) []node {
	var out []node
	for _, e := range epics {
		for _, g := range e.Groups {
			out = append(out, node{
				name:    g.Name,
				path:    groupPath(e.Number, g.Name),
				touches: g.Touches,
				files:   g.Files,
				deps:    g.Dependencies,
			})
		}
	}

	return out
}

// lintAllEpics runs every check and returns the findings with the groups each concerns.
func lintAllEpics(epics []Epic, protected, tracked []string) []scoped {
	var out []scoped
	add := func(f Finding, groups ...string) { out = append(out, scoped{f, groups}) }
	for _, e := range epics {
		for _, g := range e.Groups {
			lintBacklogGroup(e.Number, g, protected, add)
		}
	}

	nodes := epicNodes(epics)
	graph, gf := buildGraph(nodes, tracked)
	out = append(out, gf...)
	if graph.cycle() == nil {
		waveIsolation(nodes, graph, add)
	}

	caseCollisions(nodes, tracked, add)
	return out
}

// lintBacklogGroup runs the checks that look at one group alone: required, sizing, ears and protected.
func lintBacklogGroup(epic int, g Group, protected []string, add func(Finding, ...string)) {
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

// protectedPath reports whether a file is under docs/backlog or matches a protected pattern.
func protectedPath(f string, protected []string) bool {
	return f == backlogDir || strings.HasPrefix(f, backlogDir+"/") || matchesAny(protected, f)
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

// numberFindings fails a draft group that is new but does not take the next number of its epic, and a draft group whose
// status is not todo.
func numberFindingsEpics(existing, draft []Epic) []Finding {
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
