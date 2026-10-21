package contextmgr

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Slot names one hole in a template's # Brief section.
type Slot string

// The slots a template may name; the repo map slot is SlotRepoMap because RepoMap is the map type.
const (
	RepoContext  Slot = "repo_context"
	RepoProfile  Slot = "repo_profile"
	Standards    Slot = "standards"
	Group        Slot = "group"
	Tasks        Slot = "tasks"
	Files        Slot = "files"
	FileContents Slot = "file_contents"
	Diff         Slot = "diff"
	Checks       Slot = "checks"
	Findings     Slot = "findings"
	ScopeAnswer  Slot = "scope_answer"
	Unfinished   Slot = "unfinished"
	Lens         Slot = "lens"
	LensRules    Slot = "lens_rules"
	Callers      Slot = "callers"
	Specs        Slot = "specs"
	Goal         Slot = "goal"
	Layout       Slot = "layout"
	Backlog      Slot = "backlog"
	SlotRepoMap  Slot = "repo_map"
	Baseline     Slot = "baseline"
	Acceptance   Slot = "acceptance"
)

// Values holds the filled text of each slot; a missing or empty entry is an empty slot.
type Values map[Slot]string

// Part is one of the 5 brief parts, in assembly order.
type Part int

// The 5 brief parts, ordered so the parts that change least come first.
const (
	PartTools Part = iota + 1
	PartRole
	PartRepoContext
	PartStandards
	PartTask
)

// Brief is the assembled prompt, one text per part.
type Brief struct {
	Agent string
	Parts [5]string
}

// slotRow is one agent's required and optional slots.
type slotRow struct{ required, optional []Slot }

// Candidate is one link of the agent's chain with its measured context.
type Candidate struct {
	Link string
	Ctx  int
}

// EmptySlotError names the required slot that had no value.
type EmptySlotError struct{ Slot Slot }

var (
	// ErrAgentUnknown means the agent is not one of the 4.
	ErrAgentUnknown = errors.New("agent is not one of planner, builder, qa or reviewer")

	// ErrContext means no link in the chain has a ceiling that holds the brief.
	ErrContext = errors.New("brief exceeds half the measured context of every link in the chain")
)

var (
	// Agents lists the 4 agents in a fixed order.
	Agents = []string{"planner", "builder", "qa", "reviewer"}

	// slotTable names the slots each agent receives; an agent not named in a slot's row never gets it.
	slotTable = map[string]slotRow{
		"planner": {
			required: []Slot{RepoProfile, SlotRepoMap, Layout, Goal},
			optional: []Slot{RepoContext, Specs, Backlog},
		},
		"builder": {
			required: []Slot{RepoProfile, Group, Tasks, Files, FileContents},
			optional: []Slot{RepoContext, Standards, Diff, Checks, Findings, ScopeAnswer, Unfinished, Specs},
		},
		"qa": {
			required: []Slot{RepoProfile, Group, Tasks, Acceptance},
			optional: []Slot{RepoContext, Standards, Diff, Checks, Baseline},
		},
		"reviewer": {
			required: []Slot{RepoProfile, Group, Tasks, Files, Diff, Lens, LensRules, Checks},
			optional: []Slot{RepoContext, Standards, Findings, Callers, Specs, SlotRepoMap},
		},
	}
)

// Error returns the failure with the slot named.
func (e EmptySlotError) Error() string { return "required slot " + string(e.Slot) + " is empty" }

// Tokens returns the size of all 5 parts, counted with count.
func (br Brief) Tokens(count Counter) int {
	count = counterOr(count)
	n := 0
	for _, p := range br.Parts {
		n += count(p)
	}
	return n
}

// Text joins the non-empty parts with a blank line between them.
func (br Brief) Text() string {
	var parts []string

	for _, p := range br.Parts {
		if p != "" {
			parts = append(parts, p)
		}
	}

	return strings.Join(parts, "\n\n")
}

// Place returns the first candidate, in chain order, whose ceiling holds the brief.
func (br Brief) Place(chain []Candidate, count Counter) (Candidate, error) {
	size := br.Tokens(count)
	for _, c := range chain {
		if size <= Ceiling(c.Ctx) {
			return c, nil
		}
	}
	return Candidate{}, fmt.Errorf("%d tokens: %w", size, ErrContext)
}

// Ceiling returns the largest brief a link of this measured context accepts.
func Ceiling(ctx int) int { return ctx / 2 }

// buildBrief fills the template's brief section in one pass and assembles the 5 parts.
// A value for a slot outside the agent's row is dropped, an empty optional slot renders none.
func buildBrief(
	agent string,
	tpl Template,
	tools, repoCtx, standards string,
	vals Values,
) (Brief, error) {
	row, ok := slotTable[agent]
	if !ok {
		return Brief{}, fmt.Errorf("brief for %q: %w", agent, ErrAgentUnknown)
	}

	filled := Values{}

	for s, v := range vals {
		if slices.Contains(row.required, s) || slices.Contains(row.optional, s) {
			filled[s] = strings.TrimRight(v, " \t\r\n")
		}
	}

	for _, s := range row.required {
		if strings.TrimSpace(filled[s]) == "" {
			return Brief{}, EmptySlotError{Slot: s}
		}
	}

	task := slotRe.ReplaceAllStringFunc(tpl.Brief, func(hole string) string {
		v := filled[Slot(hole[2:len(hole)-2])]
		if strings.TrimSpace(v) == "" {
			return "none"
		}
		return v
	})

	return Brief{
		Agent: agent,
		Parts: [5]string{
			tools,
			tpl.Role,
			repoCtx,
			standards,
			task,
		}}, nil
}

// validAgent reports whether the name is one of the 4 agents.
func validAgent(agent string) bool { return slices.Contains(Agents, agent) }
