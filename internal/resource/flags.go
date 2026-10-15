package resource

import "github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"

// markRaised records that the error e was thrown, so the same crossing is not raised again.
func (s *State) markRaised(o Observation, e fault.ResourceError) {
	switch e.Kind {
	case "window":
		s.window(o.Key).Warned = true
	case "window_full":
		s.window(o.Key).Full = true
	case "overage":
		s.window(o.Key).Overage = true
	case "limit":
		s.window(o.Key).Limited = true
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

// errorRaised reports whether the error that goes with pause order p was thrown.
func (s *State) errorRaised(o Observation, p PauseOrder) bool {
	switch p.Reason {
	case reasonWindowFull:
		return s.window(o.Key).Full
	case reasonSpendCeiling:
		return s.Spend.RunCeilingRaised[p.Run]
	case reasonSpendCeilingDay:
		return s.Spend.DayCeilingRaised
	case reasonGroupTokens:
		return len(p.Groups) == 1 && s.Spend.GroupCapRaised[p.Groups[0]]
	}
	return false
}

// markPaused records that pause order p was delivered.
func (s *State) markPaused(o Observation, p PauseOrder) {
	switch p.Reason {
	case reasonWindowFull:
		s.window(o.Key).Paused = true
	case reasonSpendCeiling:
		s.Spend.RunCeiling[p.Run] = true
	case reasonSpendCeilingDay:
		s.Spend.DayCeiling = true
	case reasonGroupTokens:
		for _, g := range p.Groups {
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
