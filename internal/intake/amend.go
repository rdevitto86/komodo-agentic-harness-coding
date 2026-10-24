package intake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// storedPlan is a plan being changed: its blueprint and the plan.md text the planner brief carries.
type storedPlan struct {
	blueprint Blueprint
	text      string
}

// loadStored reads the blueprint and plan.md of the plan a request changes, and checks that the named group is in it.
func (h *Handler) loadStored(r PlanRequest) (storedPlan, error) {
	b, err := LoadBlueprint(h.d.Home, h.d.Repo, r.Plan)
	if err != nil {
		return storedPlan{}, err
	}
	if r.Group != "" && !slices.ContainsFunc(b.Groups, func(g PlanGroup) bool { return g.Name == r.Group }) {
		return storedPlan{}, fmt.Errorf("plan %s has no group %s", r.Plan, r.Group)
	}

	data, err := os.ReadFile(filepath.Join(PlanDir(h.d.Home, h.d.Repo, r.Plan), "plan.md"))
	if err != nil {
		return storedPlan{}, fmt.Errorf("read plan.md of %s: %w", r.Plan, err)
	}

	return storedPlan{blueprint: b, text: string(data)}, nil
}

// finishedTasks returns the names of a stored group's finished tasks, or none when no lock reader is wired.
func (h *Handler) finishedTasks(b Blueprint, group string) ([]string, error) {
	if h.d.Locks == nil {
		return nil, nil
	}

	i := slices.IndexFunc(b.Groups, func(g PlanGroup) bool { return g.Name == group })
	if i < 0 {
		return nil, nil
	}

	names, err := h.d.Locks.Finished(b.Groups[i].ID)
	if err != nil {
		return nil, fmt.Errorf("read the finished tasks of group %s: %w", group, err)
	}

	return names, nil
}

// existing returns the blueprint lint amends, or an empty one for a new plan.
func (pl *planState) existing() Blueprint {
	if pl.stored == nil {
		return Blueprint{}
	}

	return *pl.stored
}

// amendCheck holds a draft of a changed plan to its request. A group rewrite may change only its one group, and a failing
// draft goes back into the session; a shape change fails with LockedError when it removes or edits a group in a run.
func (h *Handler) amendCheck(d PlanDraft, pl *planState) ([]Finding, error) {
	if pl.stored == nil {
		return nil, nil
	}

	touched, changed := diffGroups(*pl.stored, d.blueprint())
	if pl.group != "" {
		found := rewriteFindings(pl.group, touched, changed)

		return append(found, keptFindings(pl, d.blueprint())...), nil
	}

	for _, g := range touched {
		if h.d.Locks == nil {
			return nil, errors.New("no lock reader is wired to check the groups in a run")
		}

		run, locked, err := h.d.Locks.InRun(g.ID)
		if err != nil {
			return nil, fmt.Errorf("check whether group %s is in a run: %w", g.Name, err)
		}
		if locked {
			return nil, LockedError{Run: run}
		}
	}

	return nil, nil
}

// scope returns the groups of a draft that the base-commit check must run on: all of a new plan, else the groups the draft
// adds or changes.
func (pl *planState) scope(b Blueprint) Blueprint {
	if pl.stored == nil {
		return b
	}

	_, changed := diffGroups(*pl.stored, b)
	out := b
	out.Groups = nil

	for _, g := range b.Groups {
		if slices.Contains(changed, g.Name) {
			out.Groups = append(out.Groups, g)
		}
	}

	return out
}

// diffGroups compares a draft with the stored plan by group label. touched holds the stored groups the draft removes or
// edits; changed holds the labels of the draft groups that are new or edited.
func diffGroups(stored, draft Blueprint) (touched []PlanGroup, changed []string) {
	have := map[string]PlanGroup{}
	for _, g := range stored.Groups {
		have[g.Name] = g
	}

	seen := map[string]bool{}

	for _, g := range draft.Groups {
		seen[g.Name] = true

		old, ok := have[g.Name]
		if !ok || groupText(old) != groupText(g) {
			changed = append(changed, g.Name)
		}
		if ok && groupText(old) != groupText(g) {
			touched = append(touched, old)
		}
	}

	for _, g := range stored.Groups {
		if !seen[g.Name] {
			touched = append(touched, g)
		}
	}

	return touched, changed
}

// rewriteFindings fails every change a group rewrite makes outside its one group.
func rewriteFindings(group string, touched []PlanGroup, changed []string) []Finding {
	var found []Finding

	for _, g := range touched {
		if g.Name != group {
			found = append(found, grammar(
				"plan.md#"+g.Name,
				fmt.Sprintf("a rewrite of %s must leave %s as it is", group, g.Name),
			))
		}
	}

	for _, name := range changed {
		if name == group || slices.ContainsFunc(touched, func(g PlanGroup) bool { return g.Name == name }) {
			continue
		}

		found = append(found, grammar(
			"plan.md#"+name,
			fmt.Sprintf("a rewrite of %s must not add group %s", group, name),
		))
	}

	return found
}

// keptFindings fails every finished task of the rewritten group that the draft changes or drops.
func keptFindings(pl *planState, draft Blueprint) []Finding {
	have, now := tasksOf(*pl.stored, pl.group), tasksOf(draft, pl.group)

	var found []Finding

	for _, name := range pl.finished {
		got, ok := now[name]

		switch {
		case !ok:
			found = append(found, grammar(
				"plan.md#"+pl.group,
				fmt.Sprintf("task %s is finished and must stay in %s", name, pl.group),
			))
		case taskCard(got) != taskCard(have[name]):
			found = append(found, grammar(
				"plan.md#"+pl.group,
				fmt.Sprintf("task %s is finished and must stay as it is", name),
			))
		}
	}

	return found
}

// tasksOf returns the tasks of one group of a blueprint by name.
func tasksOf(b Blueprint, group string) map[string]PlanTask {
	out := map[string]PlanTask{}

	for _, g := range b.Groups {
		if g.Name != group {
			continue
		}

		for _, t := range g.Tasks {
			out[t.Name] = t
		}
	}

	return out
}

// taskCard renders a task, so two tasks compare by what plan.md says.
func taskCard(t PlanTask) string {
	return Render(Blueprint{Groups: []PlanGroup{{Tasks: []PlanTask{t}}}})
}

// groupText renders a group without its id and hash, so two groups compare by what plan.md says.
func groupText(g PlanGroup) string {
	g.ID, g.Hash = "", ""

	return Render(Blueprint{Groups: []PlanGroup{g}})
}

// keepIDs gives each draft group that the stored plan holds the stored id.
func keepIDs(b *Blueprint, stored Blueprint) {
	ids := map[string]string{}
	for _, g := range stored.Groups {
		ids[g.Name] = g.ID
	}

	for i, g := range b.Groups {
		if id, ok := ids[g.Name]; ok {
			b.Groups[i].ID = id
		}
	}
}
