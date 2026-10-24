package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// PlannerResult is the planner's final call: the whole plan.md text.
type PlannerResult struct {
	Plan string `json:"plan" desc:"The whole plan.md text, front matter included."`
}

// PlannerRole returns the compiled planner role. It has no run tool, no write tool and no pool
// slot.
func PlannerRole() Role {
	return newRole("planner",
		[]string{"read", "search", "lint_plan"},
		[]string{},
		[]string{},
		[]string{"repo_profile", "repo_map", "layout", "goal"},
		[]string{"repo_context", "specs", "plan"},
		"komodo:agent/planner/result/1", PlannerResult{}, false)
}

// ParsePlannerResult decodes a planner final call, refusing unknown fields and an empty plan.
// Grammar rules are the lint's work.
func ParsePlannerResult(raw []byte) (PlannerResult, error) {
	var r PlannerResult
	if err := core.DecodeStrict(raw, &r); err != nil {
		return PlannerResult{}, fmt.Errorf("decode planner result: %w", err)
	}
	if strings.TrimSpace(r.Plan) == "" {
		return PlannerResult{}, errors.New("planner result has no plan")
	}

	return r, nil
}
