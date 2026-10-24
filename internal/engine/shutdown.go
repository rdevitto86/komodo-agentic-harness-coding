package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// RunOf returns the id of the live run a target names, or an empty string. The target is a run id, a plan id, or a plan id
// then a group label.
func (m *Manager) RunOf(target string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, r := range m.runs {
		if runNamed(id, r, target) {
			return id
		}
	}

	return ""
}

// Stop runs the stop sequence for one run; it always finishes within the shutdown deadline.
func (m *Manager) Stop(ctx context.Context, runID string) error {
	m.mu.Lock()

	r := m.runs[runID]
	if r == nil || r.stopping {
		m.mu.Unlock()
		if r == nil {
			return &codeError{CodeUnknownTarget, "no live run " + runID}
		}
		return nil
	}

	r.stopping = true

	groups := make([]*group, 0, len(r.groups))
	for _, g := range r.groups {
		groups = append(groups, g)
	}
	m.mu.Unlock()

	expired := make(chan struct{})
	timer := time.AfterFunc(m.t.ShutdownDeadline, func() { close(expired) })
	defer timer.Stop()

	for _, g := range groups {
		g.stopping.Store(true)
		g.send(stopCommand("stop"))
	}
	if !m.waitTMs(ctx, groups, expired) {
		m.killRemaining(r, groups)
		grace := make(chan struct{})
		gt := time.AfterFunc(killGrace, func() { close(grace) })
		m.waitTMs(ctx, groups, grace)
		gt.Stop()
	}

	for _, g := range groups {
		g.cancel()
	}

	loops := make(chan struct{})
	lt := time.AfterFunc(killGrace, func() { close(loops) })
	defer lt.Stop()

	for _, g := range groups {
		select {
		case <-g.done:
		case <-loops:
		}
	}

	var saved []bus.ID

	for _, g := range groups {
		g.mu.Lock()
		st := g.snap
		g.mu.Unlock()
		if st.Stage == NoStage || st.Stage == StageDone {
			continue
		}

		saved = append(saved, bus.ID(g.spec.ID))
		if err := m.d.Leases.Revoke(ctx, r.start.Repo, g.spec.ID, lease.RevokeStop); err != nil {
			m.report(g, fmt.Errorf("revoke the lease of %s: %w", g.spec.Label, err))
		}
	}

	m.mu.Lock()
	r.ended = true
	m.endRunLocked(r, &bus.RunStopped{Saved: orEmpty(saved)}, "stopped")
	m.mu.Unlock()
	m.schedule()
	return nil
}

// stopTarget stops every live run a run id, a plan id or a plan id then a group label names.
func (m *Manager) stopTarget(ctx context.Context, target string) error {
	m.mu.Lock()

	var ids []string

	for id, r := range m.runs {
		if runNamed(id, r, target) {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	if len(ids) == 0 {
		return &codeError{CodeUnknownTarget, "no live run named " + target}
	}

	for _, id := range ids {
		if err := m.Stop(ctx, id); err != nil {
			return err
		}
	}

	return nil
}

// runNamed reports whether the target names the run: its id, its plan id, or its plan id then a label of one of its groups.
func runNamed(id string, r *run, target string) bool {
	if id == target {
		return true
	}

	plan, label := splitTarget(target)
	if r.start.Target != plan {
		return false
	}

	return label == "" || slices.ContainsFunc(slices.Collect(maps.Values(r.specs)), func(s GroupSpec) bool {
		return s.Label == label
	})
}

// splitTarget splits "plan-7f3a9c tg-2" into the plan id and the group label; a lone word is a plan id.
func splitTarget(target string) (plan, label string) {
	f := strings.Fields(target)
	switch len(f) {
	case 0:
		return "", ""
	case 1:
		return f[0], ""
	}

	return f[0], f[1]
}

// killRemaining ends every process of the groups whose Task Manager is still running at the deadline.
func (m *Manager) killRemaining(r *run, groups []*group) {
	for _, g := range groups {
		g.mu.Lock()
		up := g.tmDone != nil
		g.mu.Unlock()
		if up && m.d.KillTree != nil {
			m.d.KillTree(r.id, g.spec.ID)
		}
	}
}

// waitTMs waits until every group's Task Manager has ended; it reports false when stop fires first.
func (m *Manager) waitTMs(ctx context.Context, groups []*group, stop <-chan struct{}) bool {
	for _, g := range groups {
		g.mu.Lock()
		done := g.tmDone
		g.mu.Unlock()
		if done == nil {
			continue
		}

		select {
		case <-done:
		case <-stop:
			return false
		case <-ctx.Done():
			return false
		}
	}

	return true
}

// busID converts text to an event identifier.
func busID(s string) bus.ID { return bus.ID(s) }
