package resource

import (
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

// Thresholds are the values the rules compare against, passed in at construction.
type Thresholds struct {
	QuotaPct         float64 // warn.quota_pct
	USDPerRun        float64 // warn.usd_per_run
	USDPerDay        float64 // warn.usd_per_day; 0 means no daily amount
	USDCeilingPerRun float64 // limits.usd_ceiling_per_run; 0 means no ceiling
	USDCeilingPerDay float64 // limits.usd_ceiling_per_day; 0 means off
	GroupTokens      int64   // limits.group_tokens; 0 means no cap and no nudge
}

// Observation names what one event changed, so Evaluate looks only at the rules it can have made true.
type Observation struct {
	Subtype bus.Subtype
	Key     WindowKey // the window, or link-level entry, the event set; empty for usage
	Link    string
	Run     string
	Group   string
	Now     time.Time
}

// Result is what one observation causes.
type Result struct {
	Errors []fault.ResourceError
	Pauses []PauseOrder
	Nudges []NudgeOrder

	keys []WindowKey // the window each error is about, in step with Errors; empty for an error about no window
}

// raise adds an error and the window it is about.
func (r *Result) raise(e fault.ResourceError, k WindowKey) {
	r.Errors = append(r.Errors, e)
	r.keys = append(r.keys, k)
}

// Evaluate returns the resource errors, pause orders and nudges one observation causes; it reads State and Thresholds and changes neither.
func Evaluate(s *State, t Thresholds, o Observation) Result {
	r := Result{}
	if o.Link != "" {
		r = evalWindows(s, t, o)
	}
	switch o.Subtype {
	case subtypeUsage:
		for _, x := range []Result{evalSpend(s, t, o), evalGroup(s, t, o)} {
			for i, e := range x.Errors {
				r.raise(e, x.keys[i])
			}
			r.Pauses = append(r.Pauses, x.Pauses...)
			r.Nudges = append(r.Nudges, x.Nudges...)
		}
	case subtypeOverage:
		if n := s.Overages[o.Link]; n != nil && !n.Raised {
			r.raise(fault.ResourceError{Kind: "overage", Severity: fault.SeverityWarn, Link: o.Link, Run: o.Run, ResetAt: n.ResetAt}, WindowKey{})
		}
	case subtypeLimit:
		if n := s.Limits[o.Key]; n != nil && !n.Raised {
			r.raise(fault.ResourceError{Kind: "limit", Severity: fault.SeverityError, Link: o.Link, Run: o.Run, ResetAt: n.ResetAt}, WindowKey{})
		}
	}
	return r
}

// evalWindows applies the plan window rules: the warning to the window a reading set, and the full-window rule to every active window of the link.
func evalWindows(s *State, t Thresholds, o Observation) Result {
	var r Result
	for _, k := range s.windowKeys(o.Link) {
		w := s.Windows[k]
		fresh := o.Subtype == subtypeHeadroom && k == o.Key
		if !fresh && !w.ResetAt.After(o.Now) {
			continue
		}
		e := fault.ResourceError{Severity: fault.SeverityWarn, Link: o.Link, Run: o.Run, Value: w.PercentUsed, ResetAt: w.ResetAt}
		if w.PercentUsed >= 100 {
			if !w.Full {
				e.Kind, e.Threshold = "window_full", 100
				r.raise(e, k)
			}
			if g := without(names(w.Groups), w.Paused); len(g) > 0 {
				r.Pauses = append(r.Pauses, PauseOrder{Reason: reasonWindowFull, Link: o.Link, Window: k.Window, Groups: g, Until: w.ResetAt})
			}
			continue
		}
		if fresh && t.QuotaPct > 0 && w.PercentUsed >= t.QuotaPct && !w.Warned {
			e.Kind, e.Threshold = "window", t.QuotaPct
			r.raise(e, k)
		}
	}
	return r
}

// evalSpend applies the paid dollar rules, per run and per day, to the totals a usage event changed.
func evalSpend(s *State, t Thresholds, o Observation) Result {
	var r Result
	sp := &s.Spend
	run, day := sp.RunUSD[o.Run], sp.DayUSD
	warn := func(kind string, usd, amount float64) {
		r.raise(fault.ResourceError{
			Kind: kind, Severity: fault.SeverityWarn, Run: o.Run, Value: usd, Threshold: amount * t.QuotaPct / 100,
		}, WindowKey{})
	}
	if t.QuotaPct > 0 && t.USDPerRun > 0 && run*100 >= t.QuotaPct*t.USDPerRun && !sp.RunWarned[o.Run] {
		warn("spend_run", run, t.USDPerRun)
	}
	if t.QuotaPct > 0 && t.USDPerDay > 0 && day*100 >= t.QuotaPct*t.USDPerDay && !sp.DayWarned {
		warn("spend_day", day, t.USDPerDay)
	}
	if c := t.USDCeilingPerRun; c > 0 && run >= c {
		if !sp.RunCeilingRaised[o.Run] {
			r.raise(fault.ResourceError{Kind: "spend_ceiling", Severity: fault.SeverityWarn, Run: o.Run, Value: run, Threshold: c}, WindowKey{})
		}
		if g := without(names(s.RunGroups[o.Run]), sp.RunCeiling[o.Run]); len(g) > 0 {
			r.Pauses = append(r.Pauses, PauseOrder{Reason: reasonSpendCeiling, Run: o.Run, Groups: g})
		}
	}
	if c := t.USDCeilingPerDay; c > 0 && day >= c {
		if !sp.DayCeilingRaised {
			r.raise(fault.ResourceError{Kind: "spend_ceiling_day", Severity: fault.SeverityWarn, Run: o.Run, Value: day, Threshold: c}, WindowKey{})
		}
		if g := without(paidGroups(s), sp.DayCeiling); len(g) > 0 {
			r.Pauses = append(r.Pauses, PauseOrder{Reason: reasonSpendCeilingDay, Groups: g, Until: nextMidnight(o.Now)})
		}
	}
	return r
}

// paidGroups returns the groups seen on every link that has reported metered dollars.
func paidGroups(s *State) []string {
	var sets []map[string]bool
	for link := range s.PaidLinks {
		sets = append(sets, s.LinkGroups[link])
	}
	return names(sets...)
}

// evalGroup applies the token cap rules to the group a usage event names.
func evalGroup(s *State, t Thresholds, o Observation) Result {
	var r Result
	g := o.Group
	if g == "" || t.GroupTokens <= 0 {
		return r
	}
	sp := &s.Spend
	tokens, limit := sp.GroupTokens[g], s.capOf(t, g)
	switch {
	case tokens >= limit:
		if !sp.GroupCapRaised[g] {
			r.raise(fault.ResourceError{
				Kind: "group_tokens", Severity: fault.SeverityWarn, Run: o.Run, Group: g, Value: float64(tokens), Threshold: float64(limit),
			}, WindowKey{})
		}
		if !sp.GroupCapped[g] {
			r.Pauses = append(r.Pauses, PauseOrder{Reason: reasonGroupTokens, Groups: []string{g}})
		}
	case tokens*10 >= limit*9 && !sp.GroupNudged[g]:
		r.Nudges = append(r.Nudges, NudgeOrder{Groups: []string{g}})
	}
	return r
}
