package resource

import "time"

// dayLayout is the form of Spend.Day.
const dayLayout = "2006-01-02"

// Spend is the paid spend and token totals.
type Spend struct {
	RunUSD      map[string]float64         // paid dollars per run
	Day         string                     // the current day, "2006-01-02" in the machine's local time zone
	DayUSD      float64                    // paid dollars today
	GroupTokens map[string]int64           // input, output and cache-write tokens per group
	RunWarned   map[string]bool            // the per-run warning was raised
	DayWarned   bool                       // the per-day warning was raised today
	RunCeiling  map[string]map[string]bool // groups whose per-run ceiling pause order was delivered, per run
	GroupCapped map[string]bool            // the group token cap pause order was delivered
	GroupNudged map[string]bool            // the 90% nudge was ordered
	CapGrants   map[string]int             // caps granted by run --approve, per group
	DayCeiling  map[string]bool            // groups whose daily ceiling pause order was delivered today

	RunCeilingRaised map[string]bool // the per-run ceiling error was raised
	GroupCapRaised   map[string]bool // the group token cap error was raised
	DayCeilingRaised bool            // the daily ceiling error was raised today
}

// State is everything the rules read.
type State struct {
	Windows    map[WindowKey]*WindowState
	Spend      Spend
	LinkGroups map[string]map[string]bool // groups seen on each link; cleared when its window resets
	RunGroups  map[string]map[string]bool // groups seen in each run
	PaidLinks  map[string]bool            // links that have reported metered dollars
	Overages   map[string]*Notice         // the overage notice of each link
	Limits     map[WindowKey]*Notice      // the refusal notice of each link and cap kind
}

// NewState returns an empty State with every map ready.
func NewState() *State {
	return &State{
		Windows: map[WindowKey]*WindowState{},
		Spend: Spend{
			RunUSD: map[string]float64{}, GroupTokens: map[string]int64{}, RunWarned: map[string]bool{},
			RunCeiling: map[string]map[string]bool{}, DayCeiling: map[string]bool{}, GroupCapped: map[string]bool{}, GroupNudged: map[string]bool{},
			CapGrants: map[string]int{}, RunCeilingRaised: map[string]bool{}, GroupCapRaised: map[string]bool{},
		},
		LinkGroups: map[string]map[string]bool{},
		RunGroups:  map[string]map[string]bool{},
		PaidLinks:  map[string]bool{},
		Overages:   map[string]*Notice{},
		Limits:     map[WindowKey]*Notice{},
	}
}

// dayOf returns the local calendar day of t.
func dayOf(t time.Time) string { return t.Local().Format(dayLayout) }

// nextMidnight returns the first local midnight after t.
func nextMidnight(t time.Time) time.Time {
	l := t.Local()
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, l.Location())
}

// rollDay starts a new day's totals and flags when the local day has changed.
func (s *State) rollDay(now time.Time) {
	d := dayOf(now)
	if s.Spend.Day == d {
		return
	}
	s.Spend.Day = d
	s.Spend.DayUSD = 0
	s.Spend.DayWarned = false
	s.Spend.DayCeiling = map[string]bool{}
	s.Spend.DayCeilingRaised = false
}

// applyUsage adds one finished session's paid dollars and tokens to the totals and returns the window keys whose session count rose.
func (s *State) applyUsage(u Usage, run, group string, now time.Time) []WindowKey {
	s.rollDay(now)
	if u.USD > 0 {
		s.Spend.RunUSD[run] += u.USD
		s.Spend.DayUSD += u.USD
		s.PaidLinks[u.Link] = true
	}
	if group != "" {
		s.Spend.GroupTokens[group] += u.TokensIn + u.TokensOut + u.CacheWrite
	}
	return s.bumpSessions(u.Link, now)
}

// capOf returns a group's token cap: the configured cap times one plus the caps granted.
func (s *State) capOf(t Thresholds, group string) int64 {
	return t.GroupTokens * int64(1+s.Spend.CapGrants[group])
}

// forgetRun drops a finished run's totals, flags and group list.
func (s *State) forgetRun(run string) {
	delete(s.Spend.RunUSD, run)
	delete(s.Spend.RunWarned, run)
	delete(s.Spend.RunCeiling, run)
	delete(s.Spend.RunCeilingRaised, run)
	delete(s.RunGroups, run)
}

// forgetGroup drops a finished group's totals and flags and removes it from every group list.
func (s *State) forgetGroup(group string) {
	delete(s.Spend.GroupTokens, group)
	delete(s.Spend.GroupNudged, group)
	delete(s.Spend.GroupCapped, group)
	delete(s.Spend.GroupCapRaised, group)
	delete(s.Spend.CapGrants, group)
	delete(s.Spend.DayCeiling, group)
	for _, g := range s.Spend.RunCeiling {
		delete(g, group)
	}
	for _, g := range s.RunGroups {
		delete(g, group)
	}
	for _, g := range s.LinkGroups {
		delete(g, group)
	}
	for _, w := range s.Windows {
		delete(w.Groups, group)
		delete(w.Paused, group)
	}
}
