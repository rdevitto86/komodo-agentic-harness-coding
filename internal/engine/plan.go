package engine

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Startable reports whether a group may start: every dependency is Done.
func Startable(depends []string, done map[string]bool) bool {
	for _, d := range depends {
		if !done[d] {
			return false
		}
	}
	return true
}

// CheckPlan refuses a dependency cycle among the groups of a run; a dependency outside the run is not part of the graph.
func CheckPlan(specs []GroupSpec) error {
	in := map[string]GroupSpec{}
	for _, s := range specs {
		in[s.ID] = s
	}
	const (
		visiting = 1
		visited  = 2
	)
	mark := map[string]int{}
	var path []string
	var walk func(id string) error
	walk = func(id string) error {
		switch mark[id] {
		case visited:
			return nil
		case visiting:
			i := slices.Index(path, id)
			return fmt.Errorf("dependency cycle among %s", strings.Join(append(slices.Clone(path[i:]), id), " -> "))
		}
		mark[id] = visiting
		path = append(path, id)
		for _, d := range in[id].Depends {
			if _, ok := in[d]; !ok {
				continue
			}
			if err := walk(d); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		mark[id] = visited
		return nil
	}
	ids := make([]string, 0, len(in))
	for id := range in {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := walk(id); err != nil {
			return err
		}
	}
	return nil
}

// Candidates returns the groups that are ready now, each with its chain weight and unblock count; done maps finished group ids.
func Candidates(specs []GroupSpec, done map[string]bool) []Candidate {
	open := map[string]GroupSpec{}
	for _, s := range specs {
		if !done[s.ID] {
			open[s.ID] = s
		}
	}
	dependents := map[string][]string{}
	for _, s := range open {
		for _, d := range s.Depends {
			if _, ok := open[d]; ok {
				dependents[d] = append(dependents[d], s.ID)
			}
		}
	}
	memo := map[string]int{}
	var chain func(id string) int
	chain = func(id string) int {
		if w, ok := memo[id]; ok {
			return w
		}
		memo[id] = 0 // a cycle adds nothing
		best := 0
		for _, c := range dependents[id] {
			best = max(best, chain(c))
		}
		memo[id] = len(open[id].Tasks) + best
		return memo[id]
	}
	var out []Candidate
	for _, s := range specs {
		if done[s.ID] || !Startable(s.Depends, done) {
			continue
		}
		unblocks := 0
		for _, c := range dependents[s.ID] {
			only := true
			for _, d := range open[c].Depends {
				if !done[d] && d != s.ID {
					only = false
				}
			}
			if only {
				unblocks++
			}
		}
		out = append(out, Candidate{Spec: s, ChainWeight: chain(s.ID), Unblocks: unblocks})
	}
	return out
}

// Pick returns the ready group to start next: high priority, then the longest chain, then the most unblocked, then task-list order.
func Pick(ready []Candidate) (Candidate, bool) {
	if len(ready) == 0 {
		return Candidate{}, false
	}
	best := ready[0]
	for _, c := range ready[1:] {
		if better(c, best) {
			best = c
		}
	}
	return best, true
}

// better reports whether a should start before b under the pick rules.
func better(a, b Candidate) bool {
	if ah, bh := a.Spec.Priority == "high", b.Spec.Priority == "high"; ah != bh {
		return ah
	}
	if a.ChainWeight != b.ChainWeight {
		return a.ChainWeight > b.ChainWeight
	}
	if a.Unblocks != b.Unblocks {
		return a.Unblocks > b.Unblocks
	}
	return a.Spec.Order < b.Spec.Order
}

// BuildLanding returns one landing plan per epic: its groups in dependency order, ties broken by task-list order.
func BuildLanding(specs []GroupSpec) []LandingPlan {
	byEpic := map[string][]GroupSpec{}
	first := map[string]int{}
	for _, s := range specs {
		if _, ok := first[s.Epic]; !ok || s.Order < first[s.Epic] {
			first[s.Epic] = s.Order
		}
		byEpic[s.Epic] = append(byEpic[s.Epic], s)
	}
	epics := make([]string, 0, len(byEpic))
	for e := range byEpic {
		epics = append(epics, e)
	}
	sort.Slice(epics, func(i, j int) bool { return first[epics[i]] < first[epics[j]] })
	var plans []LandingPlan
	for _, e := range epics {
		plans = append(plans, LandingPlan{Epic: e, Branch: "komodo/" + e, Order: landingOrder(byEpic[e])})
	}
	return plans
}

// landingOrder sorts one epic's groups so every group follows its dependencies, taking the lowest Order first.
func landingOrder(specs []GroupSpec) []string {
	left := slices.Clone(specs)
	sort.SliceStable(left, func(i, j int) bool { return left[i].Order < left[j].Order })
	placed := map[string]bool{}
	inEpic := map[string]bool{}
	for _, s := range specs {
		inEpic[s.ID] = true
	}
	order := []string{}
	for len(left) > 0 {
		pick := -1
		for i, s := range left {
			ready := true
			for _, d := range s.Depends {
				if inEpic[d] && !placed[d] {
					ready = false
				}
			}
			if ready {
				pick = i
				break
			}
		}
		if pick < 0 {
			pick = 0 // a cycle was refused earlier; place the rest in order
		}
		placed[left[pick].ID] = true
		order = append(order, left[pick].Label)
		left = slices.Delete(left, pick, pick+1)
	}
	return order
}
