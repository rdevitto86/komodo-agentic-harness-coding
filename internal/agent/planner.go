package agent

import (
	"errors"
	"fmt"
	"strings"
)

// PlannerFile is one task-list file of the planner's draft.
type PlannerFile struct {
	Path    string `json:"path"`    // path under docs/backlog/, such as epic-2/tg-2.1/TG.md
	Content string `json:"content"` // the whole file
}

// PlannerResult is the planner's final call: every task-list file of the plan.
type PlannerResult struct {
	Files []PlannerFile `json:"files"`
}

// PlannerRole returns the compiled planner role. It has no run tool, no write tool and no pool slot.
func PlannerRole() Role {
	return newRole("planner",
		[]string{"read", "search", "lint_backlog"},
		[]string{},
		[]string{},
		[]string{"repo_profile", "repo_map", "layout", "goal"},
		[]string{"repo_context", "specs", "backlog"},
		"komodo:agent/planner/result/1", []string{"files"}, false)
}

// ParsePlannerResult decodes a planner final call, refusing unknown fields, an empty plan and a file with no path.
// Path and grammar rules are the lint's work.
func ParsePlannerResult(raw []byte) (PlannerResult, error) {
	var r PlannerResult
	if err := decodeStrict(raw, &r); err != nil {
		return PlannerResult{}, fmt.Errorf("decode planner result: %w", err)
	}
	if len(r.Files) == 0 {
		return PlannerResult{}, errors.New("planner result has no files")
	}
	for i, f := range r.Files {
		if strings.TrimSpace(f.Path) == "" {
			return PlannerResult{}, fmt.Errorf("planner file %d has no path", i+1)
		}
	}
	return r, nil
}
