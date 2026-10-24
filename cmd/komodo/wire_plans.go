package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cleanup"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// planSource reads the groups of a run from the blueprints under the Komodo home.
type planSource struct {
	home   string
	repoID func(root string) (string, error) // the repository id of a root
	mu     sync.Mutex
	facts  map[string]engine.PlanFacts // facts last read per plan, kept after the plan folder is deleted
}

// newPlanSource returns a plan source over the plan folders of home.
func newPlanSource(home string, repoID func(root string) (string, error)) *planSource {
	return &planSource{home: home, repoID: repoID, facts: map[string]engine.PlanFacts{}}
}

// Specs returns the group specs of the plan for the labels, or every group of the plan when none, in plan order.
func (p *planSource) Specs(_ context.Context, root, plan string, labels []string) ([]engine.GroupSpec, error) {
	bp, err := p.blueprint(root, plan)
	if err != nil {
		return nil, err
	}

	p.remember(plan, factsOf(bp))

	ids := map[string]string{}
	for _, g := range bp.Groups {
		ids[g.Name] = g.ID
	}

	var out []engine.GroupSpec

	for i, g := range bp.Groups {
		if len(labels) > 0 && !slices.Contains(labels, g.Name) {
			continue
		}

		sp := engine.GroupSpec{
			ID:       g.ID,
			Label:    g.Name,
			Epic:     plan,
			Files:    g.Files,
			Priority: string(g.Priority),
			Order:    i + 1,
		}
		for _, d := range g.Dependencies {
			sp.Depends = append(sp.Depends, ids[d])
		}

		for _, t := range g.Tasks {
			sp.Tasks = append(sp.Tasks, t.Name)
		}

		out = append(out, sp)
	}

	for _, l := range labels {
		if !slices.ContainsFunc(out, func(s engine.GroupSpec) bool { return s.Label == l }) {
			return nil, fmt.Errorf("unknown group %s", l)
		}
	}

	return out, nil
}

// Facts returns the plan's goal, release and every group goal; a plan folder the cleanup deleted answers from the facts
// read while the plan was live.
func (p *planSource) Facts(_ context.Context, root, plan string) (engine.PlanFacts, error) {
	bp, err := p.blueprint(root, plan)
	if err == nil {
		f := factsOf(bp)
		p.remember(plan, f)

		return f, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if f, ok := p.facts[plan]; ok {
		return f, nil
	}

	return engine.PlanFacts{}, err
}

// blueprint reads the blueprint of a plan of the repository at root.
func (p *planSource) blueprint(root, plan string) (intake.Blueprint, error) {
	id, err := p.repoID(root)
	if err != nil {
		return intake.Blueprint{}, err
	}

	bp, err := intake.LoadBlueprint(p.home, intake.RepoFolder(id), plan)
	if err != nil {
		return intake.Blueprint{}, fmt.Errorf("read the plan %s: %w", plan, err)
	}

	return bp, nil
}

// remember keeps the facts of a plan read now.
func (p *planSource) remember(plan string, f engine.PlanFacts) {
	p.mu.Lock()
	p.facts[plan] = f
	p.mu.Unlock()
}

// factsOf lists the goal, the release and "<label>: <goal>" for every group of a blueprint.
func factsOf(bp intake.Blueprint) engine.PlanFacts {
	f := engine.PlanFacts{Goal: bp.Goal, Release: bp.Release}
	for _, g := range bp.Groups {
		f.GroupGoals = append(f.GroupGoals, g.Name+": "+g.Goal)
	}

	return f
}

// groupPlace is where a group id sits: its plan and its label inside the plan.
type groupPlace struct{ plan, label string }

// plannedGroups maps each group id in a repository's plan folders to its place; a folder with no readable blueprint is
// skipped.
func plannedGroups(home, folder string) map[string]groupPlace {
	out := map[string]groupPlace{}

	ents, _ := os.ReadDir(filepath.Join(home, "plans", folder))
	for _, e := range ents {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}

		bp, err := intake.LoadBlueprint(home, folder, e.Name())
		if err != nil {
			continue
		}

		for _, g := range bp.Groups {
			out[g.ID] = groupPlace{plan: e.Name(), label: g.Name}
		}
	}

	return out
}

// blueprintLabels gives the Cleanup Handler the group labels of a plan.
type blueprintLabels struct{ home, folder string }

// Labels returns the group labels of the plan; a missing blueprint wraps os.ErrNotExist.
func (b blueprintLabels) Labels(_ context.Context, plan string) ([]string, error) {
	bp, err := intake.LoadBlueprint(b.home, b.folder, plan)
	if err != nil {
		return nil, err
	}

	out := make([]string, len(bp.Groups))
	for i, g := range bp.Groups {
		out[i] = g.Name
	}

	return out, nil
}

// PlanGroup reports where the plan's group stands in the group state: closed when done, running for any other stage.
func (u unfinished) PlanGroup(_ context.Context, plan, label string) (cleanup.GroupStatus, error) {
	bp, err := intake.LoadBlueprint(u.home, u.folder, plan)
	if err != nil {
		return cleanup.StatusNone, err
	}

	i := slices.IndexFunc(bp.Groups, func(g intake.Group) bool { return g.Name == label })
	if i < 0 {
		return cleanup.StatusNone, fmt.Errorf("plan %s has no group %s", plan, label)
	}

	rec, err := u.st.Peek(bp.Groups[i].ID)
	switch {
	case errors.Is(err, state.ErrNotFound):
		return cleanup.StatusNone, nil
	case err != nil:
		return cleanup.StatusNone, err
	case rec.State.Stage == state.StageDone:
		return cleanup.StatusClosed, nil
	}

	return cleanup.StatusRunning, nil
}
