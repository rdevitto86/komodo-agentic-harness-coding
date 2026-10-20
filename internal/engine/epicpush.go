package engine

import (
	"fmt"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// epicPush is one epic's push state in a run: the done-group count its last push covered, and whether a push is running.
type epicPush struct {
	covered  int  // done groups of the epic when its last push finished; -1 before any push
	inflight bool // a push of the epic is running now
}

// settleEpicsLocked starts one push for each epic of the run that has gone quiet with more groups landed than its last push
// covered, and reports whether a push is running, so the run does not end before it. An epic is quiet when none of its groups
// works, waits for a start or can still start: each is done, failed, blocked or lost.
func (m *Manager) settleEpicsLocked(r *run) bool {
	if m.d.Commits == nil {
		return false
	}

	busy := map[string]bool{}
	done := map[string]int{}
	specs := make([]GroupSpec, 0, len(r.order))

	for _, id := range r.order {
		s := r.specs[id]
		specs = append(specs, s)

		if r.finished[id] {
			if r.done[id] {
				done[s.Epic]++
			}
			continue
		}

		g := r.groups[id]
		if g == nil {
			continue
		}

		g.mu.Lock()
		snap, dead := g.snap, g.dead
		g.mu.Unlock()

		if !dead && snap.Stage != StageBlocked {
			busy[s.Epic] = true
		}
	}

	for _, c := range Candidates(specs, r.done) {
		id := c.Spec.ID
		if r.groups[id] == nil && !r.finished[id] && !r.lost[id] && r.depBlocked[id] == "" {
			busy[c.Spec.Epic] = true
		}
	}

	pending := false

	for epic, n := range done {
		st := r.pushes[epic]
		if st == nil {
			st = &epicPush{covered: -1}
			r.pushes[epic] = st
		}

		if st.inflight {
			pending = true
			continue
		}
		if busy[epic] || st.covered == n {
			continue
		}

		st.inflight, pending = true, true
		m.spawn(func() { m.pushEpic(r, epic, n) })
	}

	return pending
}

// pushEpic asks the Commit Handler to push the epic once, publishes what it did, and checks the run again. A failed push is
// reported once and not retried until another group lands; the landed work stays in the local epic branch.
func (m *Manager) pushEpic(r *run, epic string, n int) {
	req := commit.EpicPush{
		Repo:      r.start.Repo,
		Root:      r.start.Root,
		CommonDir: r.start.CommonDir,
		Epic:      epic,
	}

	m.mu.Lock()
	if p, ok := r.landingFor(epic); ok {
		req.EpicGroups = append(req.EpicGroups, p.Order...)
	}
	m.mu.Unlock()

	res, err := m.d.Commits.PushEpic(r.ctx, req)

	m.mu.Lock()
	st := r.pushes[epic]
	st.inflight, st.covered = false, n
	m.mu.Unlock()

	switch {
	case err != nil:
		if m.d.Errors != nil {
			m.d.Errors(r.id, "", fmt.Errorf("push epic %s: %w", epic, err))
		}
	default:
		m.publishEpicPush(r, epic, res)
	}

	m.mu.Lock()
	m.signalLocked()
	m.checkLocked(r)
	m.mu.Unlock()
}

// publishEpicPush publishes the epic.pushed fact, and pr.opened when the epic's pull request is open.
func (m *Manager) publishEpicPush(r *run, epic string, res commit.EpicResult) {
	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "epic.pushed",
		Run:     bus.ID(r.id),
		Data: &bus.EpicPushed{
			Epic:   epic,
			Branch: res.Branch,
			Head:   res.Head,
			Landed: orStrings(res.Landed),
			Pushed: res.Pushed,
			Last:   res.Last,
		},
	})

	if res.PRURL == "" {
		return
	}

	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "pr.opened",
		Run:     bus.ID(r.id),
		Data:    &bus.PROpened{Epic: epic, Branch: res.Branch, Head: res.Head, PRURL: res.PRURL, PRNew: res.PRNew},
	})
}

// orStrings returns s, or an empty list for nil, so a fact prints [] and not null.
func orStrings(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
