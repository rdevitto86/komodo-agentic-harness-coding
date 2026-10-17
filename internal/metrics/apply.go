package metrics

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// groupState is a group's figures with the clocks that are still open.
type groupState struct {
	m          GroupMetrics
	last       time.Time // the time of the latest event of the group's run
	stage      string    // the stage whose clock is open; empty when none
	stageIdx   int       // index of that stage in m.Stages
	stageSince time.Time // when that clock opened
	stageBase  int64     // seconds the stage held before the clock opened
	hold       string    // outcomePaused or outcomeBlocked while a holding clock is open
	holdSince  time.Time
	holdBase   int64
}

// openSession is what a session.start remembered until its session.end.
type openSession struct {
	run                             bus.ID
	agent, task, link, model, stage string
	started                         time.Time
}

// figures holds the group and run figures of one repository, updated one event at a time.
type figures struct {
	dir      string                            // the metrics folder these figures belong to
	runs     map[bus.ID]*RunMetrics            // by run
	groups   map[bus.ID]map[bus.ID]*groupState // by run, then group
	latest   map[bus.ID]bus.ID                 // the run of each group's latest attempt
	sessions map[bus.ID]*openSession           // sessions started and not yet ended
	ended    []bus.ID                          // runs that ended since the last takeEnded
	ver      uint64                            // raised by every change
	written  uint64                            // the version the files were last written at
}

// newFigures returns empty figures.
func newFigures() *figures {
	return &figures{
		runs: map[bus.ID]*RunMetrics{}, groups: map[bus.ID]map[bus.ID]*groupState{},
		latest: map[bus.ID]bus.ID{}, sessions: map[bus.ID]*openSession{},
	}
}

// dirty reports whether a figure changed since the files were written.
func (f *figures) dirty() bool { return f.ver != f.written }

// load seeds the figures with entries read from groups.json and runs.json; their clocks stay
// closed.
func (f *figures) load(groups map[bus.ID]map[bus.ID]GroupMetrics, runs map[bus.ID]RunMetrics) {
	for id, r := range runs {
		if r.Dir == "" {
			r.Dir = f.dir
		}

		f.runs[id] = &r
	}

	for run, gs := range groups {
		for id, g := range gs {
			if f.groups[run] == nil {
				f.groups[run] = map[bus.ID]*groupState{}
			}

			f.groups[run][id] = &groupState{m: g}
			if prev, ok := f.latest[id]; !ok || f.runs[prev] == nil ||
				g.StartedAt.After(f.runs[prev].StartedAt) {
				f.latest[id] = run
			}
		}
	}
}

// snapshot returns copies of the group and run figures for writing.
func (f *figures) snapshot() (map[bus.ID]map[bus.ID]GroupMetrics, map[bus.ID]RunMetrics) {
	gs := make(map[bus.ID]map[bus.ID]GroupMetrics, len(f.groups))
	for run, byID := range f.groups {
		gs[run] = make(map[bus.ID]GroupMetrics, len(byID))
		for id, g := range byID {
			m := g.m

			m.Stages = slices.Clone(m.Stages)
			if m.Stages == nil {
				m.Stages = []StageTime{}
			}

			gs[run][id] = m
		}
	}

	rs := make(map[bus.ID]RunMetrics, len(f.runs))
	for id, r := range f.runs {
		rs[id] = *r
	}

	return gs, rs
}

// group returns a copy of one group's figures in one run.
func (f *figures) group(run, id bus.ID) (GroupMetrics, bool) {
	g, ok := f.groups[run][id]
	if !ok {
		return GroupMetrics{}, false
	}

	return g.m, true
}

// run returns a copy of one run's figures.
func (f *figures) run(id bus.ID) (RunMetrics, bool) {
	r, ok := f.runs[id]
	if !ok {
		return RunMetrics{}, false
	}

	return *r, true
}

// takeEnded returns the runs that ended since the last call.
func (f *figures) takeEnded() []bus.ID {
	out := f.ended
	f.ended = nil

	return out
}

// dropRuns forgets the figures of runs whose log was deleted.
func (f *figures) dropRuns(ids []bus.ID) {
	for _, run := range ids {
		for id := range f.groups[run] {
			if f.latest[id] == run {
				delete(f.latest, id)
			}
		}

		delete(f.groups, run)
		delete(f.runs, run)
		f.dropSessions(run)
	}

	if len(ids) > 0 {
		f.ver++
	}
}

// dropSessions forgets the open sessions of a run.
func (f *figures) dropSessions(run bus.ID) {
	for id, s := range f.sessions {
		if s.run == run {
			delete(f.sessions, id)
		}
	}
}

// ensureRun returns the run's figures, creating them at time t.
func (f *figures) ensureRun(id bus.ID, t time.Time) *RunMetrics {
	r, ok := f.runs[id]
	if !ok {
		r = &RunMetrics{Run: id, StartedAt: t, Dir: f.dir}
		f.runs[id] = r
	}

	return r
}

// ensureGroup returns the group's figures in a run, creating them at time t; a group with no id is
// not kept.
func (f *figures) ensureGroup(id, run bus.ID, t time.Time) *groupState {
	g, ok := f.groups[run][id]
	if ok {
		return g
	}

	g = &groupState{
		m: GroupMetrics{
			Group:     id,
			Run:       run,
			StartedAt: t,
			Outcome:   outcomeRunning,
			Stages:    []StageTime{},
		},
		last: t,
	}
	if id == "" {
		return g
	}

	if f.groups[run] == nil {
		f.groups[run] = map[bus.ID]*groupState{}
	}

	f.groups[run][id] = g
	if _, seen := f.latest[id]; !seen {
		f.latest[id] = run
	}

	return g
}

// touch advances every open clock of a run to time t, so seconds count up to the latest event
// copied.
func (f *figures) touch(run bus.ID, t time.Time) {
	for _, g := range f.groups[run] {
		g.last = t
		if g.stage != "" {
			g.m.Stages[g.stageIdx].Seconds = g.stageBase + secondsBetween(g.stageSince, t)
		}

		switch g.hold {
		case outcomePaused:
			g.m.PausedS = g.holdBase + secondsBetween(g.holdSince, t)
		case outcomeBlocked:
			g.m.BlockedS = g.holdBase + secondsBetween(g.holdSince, t)
		}
	}
}

// apply changes the figures for one event; it returns the session record when the event ends a
// session.
func (f *figures) apply(e bus.Event) *SessionRecord {
	if e.Run == "" {
		return nil
	}

	f.ver++
	f.ensureRun(e.Run, e.Time)
	f.touch(e.Run, e.Time)

	switch (bus.Key{Type: e.Type, Subtype: e.Subtype}) {
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "run.start"}:
		f.applyRunStart(e)
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "stage.enter"}:
		f.applyStageEnter(e)
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "group.resumed"}:
		g := f.ensureGroup(e.Group, e.Run, e.Time)
		g.closeClocks()
		g.m.Outcome = outcomeRunning
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "session.start"}:
		f.applySessionStart(e)
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "session.end"}:
		return f.applySessionEnd(e)
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "task.done"}:
		f.ensureGroup(e.Group, e.Run, e.Time).m.TasksDone++
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "task.retry"}:
		f.ensureGroup(e.Group, e.Run, e.Time).m.Retries++
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "group.paused"}:
		f.ensureGroup(e.Group, e.Run, e.Time).openHold(outcomePaused, e.Time)
	case bus.Key{Type: bus.TypeRunFailure, Subtype: "group.blocked"}:
		f.ensureGroup(e.Group, e.Run, e.Time).openHold(outcomeBlocked, e.Time)
	case bus.Key{Type: bus.TypeRunStatus, Subtype: "group.done"}:
		f.ensureGroup(e.Group, e.Run, e.Time).finish(groupOutcome(e.Data), e.Time)
	case bus.Key{Type: bus.TypeRunFailure, Subtype: "lease.lost"}:
		f.ensureGroup(e.Group, e.Run, e.Time).finish(outcomeFailed, e.Time)
	case bus.Key{Type: bus.TypeRunFailure, Subtype: "stalled"}:
		f.applyStalled(e)
	case bus.Key{Type: bus.TypeRunComplete, Subtype: "done"}:
		f.applyRunDone(e)
	case bus.Key{Type: bus.TypeRunComplete, Subtype: "stopped"}:
		f.endRun(e.Run, e.Time, outcomeStopped)
	default:
		if e.Type == bus.TypeError {
			f.applyError(e)
		}
	}

	return nil
}

// applyRunStart creates the run entry and one group entry per queued group, closing an earlier
// attempt of a group as interrupted.
func (f *figures) applyRunStart(e bus.Event) {
	d, ok := e.Data.(*bus.RunStartedFact)
	if !ok {
		return
	}

	f.runs[e.Run].Groups = len(d.Groups)
	for _, id := range d.Groups {
		if prev, seen := f.latest[id]; seen && prev != e.Run {
			if pg := f.groups[prev][id]; pg != nil && pg.m.EndedAt.IsZero() {
				at := pg.last
				if at.IsZero() {
					at = pg.m.StartedAt
				}

				pg.finish(outcomeInterrupted, at)
				f.endRunIfSettled(prev)
			}
		}

		f.latest[id] = e.Run
		f.ensureGroup(id, e.Run, e.Time)
	}
}

// applyStageEnter closes the open clocks, opens the new stage's clock and counts a return.
func (f *figures) applyStageEnter(e bus.Event) {
	d, ok := e.Data.(*bus.StageEnter)
	if !ok {
		return
	}

	g := f.ensureGroup(e.Group, e.Run, e.Time)
	g.closeClocks()
	g.m.Outcome = outcomeRunning
	g.openStage(d.To, e.Time)

	if d.Return {
		g.m.Returns++
	}
}

// applySessionStart remembers the session's details and counts it.
func (f *figures) applySessionStart(e bus.Event) {
	d, ok := e.Data.(*bus.SessionStart)
	if !ok {
		return
	}

	f.ensureGroup(e.Group, e.Run, e.Time).m.Sessions++
	f.sessions[d.Session] = &openSession{
		run:     e.Run,
		agent:   d.Agent,
		task:    d.Task,
		link:    d.Link,
		model:   d.Model,
		stage:   d.Stage,
		started: e.Time,
	}
}

// applySessionEnd adds the session's usage to its group and run and returns the session record.
func (f *figures) applySessionEnd(e bus.Event) *SessionRecord {
	d, ok := e.Data.(*bus.SessionEnd)
	if !ok {
		return nil
	}

	g := f.ensureGroup(e.Group, e.Run, e.Time)

	rec := &SessionRecord{
		Session: d.Session,
		Run:     e.Run,
		Group:   e.Group,
		Reason:  d.Reason,
		WallS:   d.WallS,
		EndedAt: e.Time,
	}
	if s, found := f.sessions[d.Session]; found {
		rec.Agent, rec.Task, rec.Link = s.agent, s.task, s.link
		rec.Model, rec.Stage, rec.StartedAt = s.model, s.stage, s.started

		delete(f.sessions, d.Session)
	} else {
		g.m.Sessions++
	}

	if d.Usage != nil {
		u := *d.Usage
		rec.Usage = &u
		g.m.Usage = addUsage(g.m.Usage, u)
		r := f.runs[e.Run]
		r.Usage = addUsage(r.Usage, u)
	}

	return rec
}

// applyRunDone sets the outcomes the event lists, closes every clock of the run and ends it.
func (f *figures) applyRunDone(e bus.Event) {
	d, ok := e.Data.(*bus.RunDone)
	if !ok {
		return
	}

	for _, id := range d.Done {
		f.ensureGroup(id, e.Run, e.Time).m.Outcome = outcomeDone
	}

	for _, id := range d.Blocked {
		f.ensureGroup(id, e.Run, e.Time).m.Outcome = outcomeBlocked
	}

	f.endRun(e.Run, e.Time, "")
	r := f.runs[e.Run]
	r.Done, r.Blocked = len(d.Done), len(d.Blocked)
}

// applyStalled ends the run: blocked groups stay blocked and every other open group fails.
func (f *figures) applyStalled(e bus.Event) {
	for _, g := range f.groups[e.Run] {
		if g.m.EndedAt.IsZero() && g.m.Outcome == outcomeBlocked {
			g.finish("", e.Time)
		}
	}

	f.endRun(e.Run, e.Time, outcomeFailed)
}

// endRun ends every open group of the run with outcome (or keeps theirs when empty), then the run
// itself.
func (f *figures) endRun(run bus.ID, t time.Time, outcome string) {
	for _, g := range f.groups[run] {
		if g.m.EndedAt.IsZero() {
			g.finish(outcome, t)
		}
	}

	f.closeRun(run, t)
}

// endRunIfSettled ends a run once all its groups have ended, at the latest group end.
func (f *figures) endRunIfSettled(run bus.ID) {
	var last time.Time

	for _, g := range f.groups[run] {
		if g.m.EndedAt.IsZero() {
			return
		}

		if g.m.EndedAt.After(last) {
			last = g.m.EndedAt
		}
	}

	f.closeRun(run, last)
}

// closeRun sets the end time and wall time of a run that has not ended and forgets its open
// sessions.
func (f *figures) closeRun(run bus.ID, t time.Time) {
	r, ok := f.runs[run]
	if !ok || !r.EndedAt.IsZero() {
		return
	}

	r.EndedAt, r.WallS = t, secondsBetween(r.StartedAt, t)

	f.ended = append(f.ended, run)
	f.dropSessions(run)
	f.ver++
}

// applyError raises the run's fault or warning count by the event's severity.
func (f *figures) applyError(e bus.Event) {
	b, err := json.Marshal(e.Data)
	if err != nil {
		return
	}

	var body struct {
		Severity string `json:"severity"`
	}

	_ = json.Unmarshal(b, &body)
	switch body.Severity {
	case "error":
		f.runs[e.Run].Faults++
	case "warn":
		f.runs[e.Run].Warnings++
	}
}

// closeClocks stops the group's stage and holding clocks; touch has already counted up to now.
func (g *groupState) closeClocks() {
	g.stage, g.hold = "", ""
}

// finish ends the group at time t with outcome; an outcome of "" keeps the current one.
func (g *groupState) finish(outcome string, t time.Time) {
	g.closeClocks()

	if outcome != "" {
		g.m.Outcome = outcome
	}

	if g.m.EndedAt.IsZero() {
		g.m.EndedAt = t
	}
}

// openStage starts the clock of stage name at time t.
func (g *groupState) openStage(name string, t time.Time) {
	idx := slices.IndexFunc(g.m.Stages, func(s StageTime) bool { return s.Stage == name })
	if idx < 0 {
		g.m.Stages = append(g.m.Stages, StageTime{Stage: name})
		idx = len(g.m.Stages) - 1
	}

	g.m.Stages[idx].Entries++
	g.stage, g.stageIdx, g.stageSince, g.stageBase = name, idx, t, g.m.Stages[idx].Seconds
}

// openHold closes the stage clock and starts the paused or blocked clock at time t.
func (g *groupState) openHold(kind string, t time.Time) {
	g.closeClocks()
	g.hold, g.holdSince = kind, t

	g.holdBase = g.m.PausedS
	if kind == outcomeBlocked {
		g.holdBase = g.m.BlockedS
	}

	g.m.Outcome = kind
}

// secondsBetween returns whole seconds from a to b, never negative.
func secondsBetween(a, b time.Time) int64 {
	return max(int64(b.Sub(a)/time.Second), 0)
}

// groupOutcome maps a group.done body to a group outcome: complete is done, abandoned stays, lost
// is failed.
func groupOutcome(p bus.Payload) string {
	d, ok := p.(*bus.GroupDone)
	if !ok {
		return outcomeDone
	}

	switch d.Outcome {
	case "abandoned":
		return outcomeAbandoned
	case "lost":
		return outcomeFailed
	default:
		return outcomeDone
	}
}
