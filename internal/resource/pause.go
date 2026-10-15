package resource

import (
	"slices"
	"time"
)

// The reasons of a pause order.
const (
	reasonWindowFull      = "window_full"
	reasonSpendCeiling    = "spend_ceiling"
	reasonSpendCeilingDay = "spend_ceiling_day"
	reasonGroupTokens     = "group_tokens"
)

// PauseOrder asks the Runtime Manager to pause groups after their current step.
type PauseOrder struct {
	Reason string    `json:"reason"`           // window_full, spend_ceiling, spend_ceiling_day or group_tokens
	Link   string    `json:"link,omitempty"`   // the link whose window is full
	Window string    `json:"window,omitempty"` // the window that is full
	Run    string    `json:"run,omitempty"`    // the run whose ceiling was reached
	Groups []string  `json:"groups"`           // groups seen on the link or in the run, on a paid link for spend_ceiling_day, or the capped group
	Until  time.Time `json:"until,omitzero"`   // the window's reset time, or the next local midnight for spend_ceiling_day; zero otherwise
}

// NudgeOrder asks the Runtime Manager to nudge the running agents of groups at 90% of the token cap.
type NudgeOrder struct {
	Groups []string `json:"groups"`
}

// seen adds group to the group lists of its link, its run and each window of its link; an empty group adds nothing.
func (s *State) seen(link, run, group string) {
	if group == "" {
		return
	}
	add(s.LinkGroups, link, group)
	add(s.RunGroups, run, group)
	for _, k := range s.windowKeys(link) {
		s.Windows[k].Groups[group] = true
	}
}

// without returns the groups that are not in done.
func without(groups []string, done map[string]bool) []string {
	var out []string
	for _, g := range groups {
		if !done[g] {
			out = append(out, g)
		}
	}
	return out
}

// add puts group in the set under key, creating the set when it is missing.
func add(m map[string]map[string]bool, key, group string) {
	if m[key] == nil {
		m[key] = map[string]bool{}
	}
	m[key][group] = true
}

// names returns the members of the given sets, once each and in order.
func names(sets ...map[string]bool) []string {
	var out []string
	for _, set := range sets {
		for g := range set {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
