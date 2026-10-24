package engine

import (
	"errors"
	"fmt"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// epicPush is one epic's push state in a run: the done-group count its last push covered, and whether a push is running.
type epicPush struct {
	covered  int    // done groups of the epic when its last push finished; -1 before any push
	inflight bool   // a push of the epic is running now
	err      string // why the last push failed for good; empty when it succeeded
}

// settleEpicsLocked starts one push for each epic of the run that has gone quiet with more groups landed than its last push
// covered, and reports whether a push is running, so the run does not end before it. An epic is quiet when none of its
// groups works, waits for a start or can still start: each is done, failed, blocked or lost.
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
	for _, id := range r.order {
		if s := r.specs[id]; s.Epic == epic && r.done[id] {
			req.Done = append(req.Done, s.Label)
		}
	}
	m.mu.Unlock()

	var res commit.EpicResult

	facts, err := m.d.Plans.Facts(r.ctx, r.start.Root, epic)
	if err != nil {
		err = fmt.Errorf("read the plan %s: %w", epic, err)
	} else {
		req.Goal, req.GroupGoals, req.Release = facts.Goal, facts.GroupGoals, facts.Release
		res, err = m.tryPush(r, req)
	}

	m.mu.Lock()
	st := r.pushes[epic]
	st.inflight, st.covered, st.err = false, n, ""

	if err != nil {
		st.err = err.Error()
	}
	m.mu.Unlock()

	switch {
	case err != nil:
		m.reportPushFailure(r, epic, err)
	default:
		m.publishEpicPush(r, epic, res)
		if res.CleanupError != "" && m.d.Errors != nil {
			m.d.Errors(r.id, "", fmt.Errorf("clean up epic %s: %s", epic, res.CleanupError))
		}
	}

	m.mu.Lock()
	m.signalLocked()
	m.checkLocked(r)
	m.mu.Unlock()
}

// tryPush asks for the push up to PushTries times, waiting twice as long after each try, and stops at a failure a repeat
// cannot fix or when the run ends.
func (m *Manager) tryPush(r *run, req commit.EpicPush) (commit.EpicResult, error) {
	wait := m.t.PushRetryEvery

	for try := 1; ; try++ {
		res, err := m.d.Commits.PushEpic(r.ctx, req)

		var pe *commit.PublishError
		if err == nil || try >= PushTries || !errors.As(err, &pe) || !pe.Retryable {
			return res, err
		}

		gone := make(chan struct{})
		t := m.clk.AfterFunc(wait, func() { close(gone) })

		select {
		case <-gone:
		case <-r.ctx.Done():
			t.Stop()
			return res, err
		}

		wait *= 2
	}
}

// reportPushFailure reports a push that failed for good to the Error Handler and publishes a stalled fact naming the epic,
// so the run ends with the epic's groups blocked and run-complete is never taken for a pull request.
func (m *Manager) reportPushFailure(r *run, epic string, err error) {
	if m.d.Errors != nil {
		m.d.Errors(r.id, "", fmt.Errorf("push epic %s: %w", epic, err))
	}

	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunFailure,
		Subtype: "stalled",
		Run:     bus.ID(r.id),
		Data:    &Stalled{Groups: []StalledGroup{{Group: epic, Reason: "unpushed", On: err.Error()}}},
	})
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
