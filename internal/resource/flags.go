package resource

import "github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"

// markRaised records that the error e was thrown, so the same crossing is not raised again;
// k is the window the error is about.
func (s *State) markRaised(o Observation, k WindowKey, e fault.ResourceError) {
	switch e.Kind {
	case "window":
		s.Windows[k].Warned = true
	case "window_full":
		s.Windows[k].Full = true
	case "overage":
		markNotice(s.Overages[o.Link])
	case "limit":
		markNotice(s.Limits[o.Key])
	case "spend_run":
		s.Spend.RunWarned[e.Run] = true
	case "spend_day":
		s.Spend.DayWarned = true
	case "spend_ceiling":
		s.Spend.RunCeilingRaised[e.Run] = true
	case "spend_ceiling_day":
		s.Spend.DayCeilingRaised = true
	case "group_tokens":
		s.Spend.GroupCapRaised[e.Group] = true
	}
}

// markNotice keeps a notice raised until its reset time; a notice with no reset time is never
// kept, so it raises every time.
func markNotice(n *Notice) {
	if n != nil && !n.ResetAt.IsZero() {
		n.Raised = true
	}
}

// errorRaised reports whether the error that goes with pause order p was thrown.
func (s *State) errorRaised(p PauseOrder) bool {
	switch p.Reason {
	case reasonWindowFull:
		return s.Windows[WindowKey{Link: p.Link, Window: p.Window}].Full
	case reasonSpendCeiling:
		return s.Spend.RunCeilingRaised[p.Run]
	case reasonSpendCeilingDay:
		return s.Spend.DayCeilingRaised
	case reasonGroupTokens:
		return len(p.Groups) == 1 && s.Spend.GroupCapRaised[p.Groups[0]]
	}

	return false
}

// markPaused records that pause order p was delivered for each group it names.
func (s *State) markPaused(p PauseOrder) {
	for _, g := range p.Groups {
		switch p.Reason {
		case reasonWindowFull:
			s.Windows[WindowKey{Link: p.Link, Window: p.Window}].Paused[g] = true
		case reasonSpendCeiling:
			if s.Spend.RunCeiling[p.Run] == nil {
				s.Spend.RunCeiling[p.Run] = map[string]bool{}
			}

			s.Spend.RunCeiling[p.Run][g] = true
		case reasonSpendCeilingDay:
			s.Spend.DayCeiling[g] = true
		case reasonGroupTokens:
			s.Spend.GroupCapped[g] = true
		}
	}
}

// markNudged records that the nudge n was delivered.
func (s *State) markNudged(n NudgeOrder) {
	for _, g := range n.Groups {
		s.Spend.GroupNudged[g] = true
	}
}
