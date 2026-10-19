package contextmgr

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Template is one agent's prompt file split into its role text and its brief section.
type Template struct {
	Agent string
	Role  string // the # Role section, heading line included
	Brief string // the # Brief section, heading line included, with its {{slot}} holes
	Slots []Slot // the holes of Brief in order of first appearance
}

// ErrTemplate means a template names an unknown slot or has no # Brief section.
var ErrTemplate = errors.New("template is invalid")

var (
	// slotRe finds every {{...}} hole, well formed or not.
	slotRe = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

	// knownSlots holds every slot name a template may use.
	knownSlots = map[Slot]bool{
		RepoContext:  true,
		RepoProfile:  true,
		Standards:    true,
		Group:        true,
		Tasks:        true,
		Files:        true,
		FileContents: true,
		Diff:         true,
		Checks:       true,
		Findings:     true,
		ScopeAnswer:  true,
		Unfinished:   true,
		Lens:         true,
		LensRules:    true,
		Callers:      true,
		Specs:        true,
		Goal:         true,
		Layout:       true,
		Backlog:      true,
		SlotRepoMap:  true,
		Baseline:     true,
		Acceptance:   true,
	}
)

// ParseTemplate splits a template and checks its holes against the agent's slot row.
func ParseTemplate(agent, text string) (Template, error) {
	row, ok := slotTable[agent]
	if !ok {
		return Template{}, fmt.Errorf("template for %q: %w", agent, ErrAgentUnknown)
	}

	role, brief, err := splitTemplate(text)
	if err != nil {
		return Template{}, err
	}
	if slotRe.MatchString(role) {
		return Template{}, fmt.Errorf("%w: the role text holds a slot", ErrTemplate)
	}

	tpl := Template{Agent: agent, Role: role, Brief: brief}

	for _, m := range slotRe.FindAllStringSubmatch(brief, -1) {
		s := Slot(m[1])
		if !knownSlots[s] {
			return Template{}, fmt.Errorf("%w: unknown slot %q", ErrTemplate, m[1])
		}

		if s == RepoContext || s == Standards {
			return Template{}, fmt.Errorf(
				"%w: slot %q is filled in part 3 or 4 and takes no hole",
				ErrTemplate,
				m[1],
			)
		}

		if !slices.Contains(row.required, s) && !slices.Contains(row.optional, s) {
			return Template{}, fmt.Errorf(
				"%w: slot %q is not named in the %s row",
				ErrTemplate,
				m[1],
				agent,
			)
		}

		if !slices.Contains(tpl.Slots, s) {
			tpl.Slots = append(tpl.Slots, s)
		}
	}

	for _, s := range row.required {
		if !slices.Contains(tpl.Slots, s) {
			return Template{}, fmt.Errorf("%w: required slot %q has no hole", ErrTemplate, s)
		}
	}

	return tpl, nil
}

// splitTemplate cuts a template into its # Role and # Brief sections, ignoring headings inside
// code fences.
func splitTemplate(text string) (role, brief string, err error) {
	var role2, brief2 strings.Builder
	mode, fence := 0, false

	for _, line := range strings.SplitAfter(text, "\n") {
		bare := strings.TrimRight(line, " \t\r\n")

		switch {
		case isFence(line):
			fence = !fence
		case fence:
		case bare == "# Role" && mode == 0:
			mode = 1
		case bare == "# Brief" && mode == 1:
			mode = 2
		case strings.HasPrefix(bare, "# "):
			return "", "", fmt.Errorf("%w: unexpected section %q", ErrTemplate, bare)
		}

		switch mode {
		case 1:
			role2.WriteString(line)
		case 2:
			brief2.WriteString(line)
		default:
			if bare != "" {
				return "", "", fmt.Errorf("%w: text before # Role", ErrTemplate)
			}
		}
	}

	if mode < 2 {
		return "", "", fmt.Errorf(
			"%w: needs a # Role section and then a # Brief section",
			ErrTemplate,
		)
	}

	return strings.TrimRight(role2.String(), " \t\r\n"),
		strings.TrimRight(brief2.String(), " \t\r\n") + "\n",
		nil
}
