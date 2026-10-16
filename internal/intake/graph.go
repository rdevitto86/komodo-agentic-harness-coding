package intake

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// Limits that make a graph finding.
const (
	maxChain        = 4 // groups on a dependency chain before lint warns
	maxDependencies = 5 // dependencies of one group before lint warns
	minForWaitedOn  = 3 // groups needed before "every other group waits on one" means anything
)

// Graph is the dependency graph of the task lists: groups are nodes, "must finish first" are edges.
type Graph struct {
	Groups  []string            // group names in folder-name order
	Deps    map[string][]string // group name -> groups that must finish first
	Touches map[string][]string // group name -> its touches globs
}

// BuildGraph builds the dependency graph; a cycle is a failing graph finding.
// An unknown dependency, two groups with overlapping touches and no order, and a long chain, a group all others wait on or a group with many dependencies are findings too.
func BuildGraph(epics []Epic, tracked []string) (Graph, []Finding) {
	g, found := buildGraph(epics, tracked)
	return g, strip(found)
}

// buildGraph builds the graph and returns each finding with every group it names.
func buildGraph(epics []Epic, tracked []string) (Graph, []scoped) {
	g := Graph{Deps: map[string][]string{}, Touches: map[string][]string{}}
	paths := map[string]string{}
	var files []string
	files = append(files, tracked...)
	for _, e := range epics {
		for _, gr := range e.Groups {
			g.Groups = append(g.Groups, gr.Name)
			paths[gr.Name] = groupPath(e.Number, gr.Name)
			g.Touches[gr.Name] = gr.Touches
			files = append(files, gr.Files...)
		}
	}
	var found []scoped
	for _, e := range epics {
		for _, gr := range e.Groups {
			g.Deps[gr.Name] = []string{}
			for _, d := range gr.Dependencies {
				if _, ok := paths[d]; !ok {
					found = append(found, graphFinding(SeverityFail, paths[gr.Name], fmt.Sprintf("%s depends on %s, which is not a group", gr.Name, d), gr.Name))
					continue
				}
				g.Deps[gr.Name] = append(g.Deps[gr.Name], d)
			}
			if n := len(gr.Dependencies); n > maxDependencies {
				found = append(found, graphFinding(SeverityWarn, paths[gr.Name], fmt.Sprintf("%s has %d dependencies; more than %d", gr.Name, n, maxDependencies), gr.Name))
			}
		}
	}
	if cycle := g.cycle(); cycle != nil {
		found = append(found, graphFinding(SeverityFail, paths[cycle[0]], "dependency cycle: "+strings.Join(cycle, " -> "), cycle...))
		return g, found
	}
	found = append(found, g.unordered(paths, files)...)
	found = append(found, g.shape(paths)...)
	return g, found
}

// graphFinding returns a graph finding of the given severity that names the given groups.
func graphFinding(s Severity, path, msg string, groups ...string) scoped {
	return scoped{Finding{Rule: RuleGraph, Severity: s, Path: path, Message: msg}, groups}
}

// cycle returns the groups of one dependency cycle, first group repeated at the end, or nil.
func (g Graph) cycle() []string {
	state := map[string]int{} // 1 on the current path, 2 done
	var stack []string
	var walk func(n string) []string
	walk = func(n string) []string {
		state[n] = 1
		stack = append(stack, n)
		for _, d := range g.Deps[n] {
			if state[d] == 1 {
				i := slices.Index(stack, d)
				return append(slices.Clone(stack[i:]), d)
			}
			if state[d] == 0 {
				if c := walk(d); c != nil {
					return c
				}
			}
		}
		state[n] = 2
		stack = stack[:len(stack)-1]
		return nil
	}
	for _, n := range g.Groups {
		if state[n] == 0 {
			if c := walk(n); c != nil {
				return c
			}
		}
	}
	return nil
}

// reaches reports whether group from waits, directly or through other groups, on group to.
func (g Graph) reaches(from, to string) bool {
	seen := map[string]bool{}
	var walk func(n string) bool
	walk = func(n string) bool {
		for _, d := range g.Deps[n] {
			if d == to || (!seen[d] && walk(d)) {
				return true
			}
			seen[d] = true
		}
		return false
	}
	return walk(from)
}

// unordered fails each pair of groups whose touches globs match one file when neither waits on the other.
func (g Graph) unordered(paths map[string]string, files []string) []scoped {
	files = slices.Clone(files)
	slices.Sort(files)
	files = slices.Compact(files)
	var found []scoped
	for i, a := range g.Groups {
		for _, b := range g.Groups[i+1:] {
			if g.reaches(a, b) || g.reaches(b, a) {
				continue
			}
			if f := sharedFile(g.Touches[a], g.Touches[b], files); f != "" {
				found = append(found, graphFinding(SeverityFail, paths[b], fmt.Sprintf("%s and %s both touch %s and neither depends on the other", a, b, f), a, b))
			}
		}
	}
	return found
}

// sharedFile returns the first file, in byte order, that a glob of as and a glob of bs both match.
func sharedFile(as, bs, files []string) string {
	for _, f := range files {
		if matchesAny(as, f) && matchesAny(bs, f) {
			return f
		}
	}
	return ""
}

// matchesAny reports whether the path matches one of the globs.
func matchesAny(globs []string, p string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return agent.MatchPath(g, p) })
}

// shape warns about a chain over 4 groups and about a group every other group waits on directly.
func (g Graph) shape(paths map[string]string) []scoped {
	var found []scoped
	if n := g.LongestChain(); n > maxChain {
		chain := g.longestPath()
		found = append(found, graphFinding(SeverityWarn, paths[chain[len(chain)-1]], fmt.Sprintf("dependency chain of %d groups (%s); more than %d", n, strings.Join(chain, ", "), maxChain), chain...))
	}
	if len(g.Groups) >= minForWaitedOn {
		for _, n := range g.Groups {
			waiting := 0
			for _, m := range g.Groups {
				if slices.Contains(g.Deps[m], n) {
					waiting++
				}
			}
			if waiting == len(g.Groups)-1 {
				found = append(found, graphFinding(SeverityWarn, paths[n], fmt.Sprintf("every other group waits on %s", n), n))
			}
		}
	}
	return found
}

// waveOf returns each group's wave: 1 with no dependency, else 1 plus the highest wave it waits on.
func (g Graph) waveOf() map[string]int {
	wave := map[string]int{}
	var of func(n string) int
	of = func(n string) int {
		if w, ok := wave[n]; ok {
			return w
		}
		wave[n] = 1 // guards a cycle; BuildGraph refuses one
		w := 1
		for _, d := range g.Deps[n] {
			w = max(w, of(d)+1)
		}
		wave[n] = w
		return w
	}
	for _, n := range g.Groups {
		of(n)
	}
	return wave
}

// Waves returns the groups that can run together; wave 1 holds the groups with no dependency.
func (g Graph) Waves() [][]string {
	wave := g.waveOf()
	var out [][]string
	for _, n := range g.Groups {
		for len(out) < wave[n] {
			out = append(out, []string{})
		}
		out[wave[n]-1] = append(out[wave[n]-1], n)
	}
	return out
}

// LongestChain returns the number of groups on the longest dependency chain.
func (g Graph) LongestChain() int {
	n := 0
	for _, w := range g.waveOf() {
		n = max(n, w)
	}
	return n
}

// longestPath returns the groups of one longest chain, first to last.
func (g Graph) longestPath() []string {
	wave := g.waveOf()
	end := ""
	for _, n := range g.Groups {
		if end == "" || wave[n] > wave[end] {
			end = n
		}
	}
	var chain []string
	for n := end; n != ""; {
		chain = append([]string{n}, chain...)
		next := ""
		for _, d := range g.Deps[n] {
			if wave[d] == wave[n]-1 {
				next = d
				break
			}
		}
		n = next
	}
	return chain
}

// ordered returns every group, wave first and then folder-name order.
func (g Graph) ordered() []string {
	var out []string
	for _, w := range g.Waves() {
		out = append(out, w...)
	}
	return out
}

// Pending returns the labels of every group under target whose status is not done, in dependency order; target is all, an epic label or a group label.
func Pending(epics []Epic, g Graph, target string) []string {
	inScope := map[string]bool{}
	for _, e := range epics {
		for _, gr := range e.Groups {
			if gr.Status == "done" {
				continue
			}
			if target == "all" || target == gr.Name || target == fmt.Sprintf("ep-%d", e.Number) {
				inScope[gr.Name] = true
			}
		}
	}
	out := []string{}
	for _, n := range g.ordered() {
		if inScope[n] {
			out = append(out, n)
		}
	}
	return out
}
