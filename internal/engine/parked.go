package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// ParkedGroup names a saved group whose run is not live, so the engine can look at its record.
type ParkedGroup struct {
	Repo      string // the repository id
	Root      string // the repository root on this machine
	CommonDir string // the repository's git common directory
	Group     string // the group id
}

// ParkedSource lists the saved groups the engine could not have loaded through a run, such as those a lease still names.
type ParkedSource func(ctx context.Context) []ParkedGroup

// RearmOverrides loads every group saved blocked on retries with a deadline and arms its override timer from that deadline;
// a deadline that passed while the engine was down lapses at once.
func (m *Manager) RearmOverrides(ctx context.Context) {
	if m.d.Parked == nil {
		return
	}

	for _, p := range m.d.Parked(ctx) {
		rec, err := m.parkedRecord(ctx, p)
		if err != nil || !awaitsOverride(rec.State) {
			continue
		}

		m.mu.Lock()
		live := m.groups[p.Group] != nil || m.parked[p.Group] != nil
		m.mu.Unlock()
		if live {
			continue
		}

		wait := rec.State.Block.Until.Sub(m.clk.Now())
		if wait <= 0 {
			m.lapseParked(ctx, p)
			continue
		}

		m.mu.Lock()
		m.parked[p.Group] = m.clk.AfterFunc(wait, func() { m.lapseParked(m.runCtx(), p) })
		m.signalLocked()
		m.mu.Unlock()
	}
}

// awaitsOverride reports whether a group waits for a person to override within a window.
func awaitsOverride(s State) bool {
	return s.Stage == StageBlocked && s.Block.Reason == state.ReasonRetries && !s.Block.Until.IsZero()
}

// parkedRecord loads the saved record of a parked group.
func (m *Manager) parkedRecord(ctx context.Context, p ParkedGroup) (state.Record, error) {
	store, err := m.d.States(p.CommonDir)
	if err != nil {
		return state.Record{}, fmt.Errorf("open the state of %s: %w", p.Repo, err)
	}
	return store.Load(ctx, p.Group)
}

// runCtx returns the context the manager runs under.
func (m *Manager) runCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}

// lapseParked fails a parked group whose override window passed: the saved record is read again, the lapse is decided by
// Next, and the result is saved, published and its lease released. A group a run has taken since is left to that run.
func (m *Manager) lapseParked(ctx context.Context, p ParkedGroup) {
	m.mu.Lock()
	delete(m.parked, p.Group)
	live := m.groups[p.Group] != nil
	m.signalLocked()
	m.mu.Unlock()
	if live || ctx.Err() != nil {
		return
	}

	if err := m.failParked(ctx, p); err != nil {
		m.reportErr("", p.Group, fmt.Errorf("lapse the override of %s: %w", p.Group, err))
	}
}

// failParked applies the override_lapse timer to the saved record of a parked group.
func (m *Manager) failParked(ctx context.Context, p ParkedGroup) error {
	store, err := m.d.States(p.CommonDir)
	if err != nil {
		return err
	}

	rec, err := store.Load(ctx, p.Group)
	if err != nil {
		return err
	}
	if !awaitsOverride(rec.State) {
		return nil
	}

	l, err := LoadLimits(m.d.Config)
	if err != nil {
		return err
	}

	now := m.clk.Now()
	in := Input{Kind: InTimerDue, Group: p.Group, Time: now, Data: mustJSON(TimerDue{Reason: "override_lapse"})}

	res := Next(rec.State, in, l)
	if res.Refused || res.State.Stage != StageDone {
		return errors.New("the override window has not passed")
	}

	res.State.ActionsOpen = []state.OpenAction{}
	if _, err := store.Save(ctx, p.Group, res.State, rec.Header.Rev); err != nil {
		return err
	}

	for _, e := range res.Emit {
		if err := m.d.Bus.Publish(e); err != nil {
			m.reportErr(rec.State.Run, p.Group, err)
		}
	}

	if lease, ok, err := m.d.Leases.Holder(ctx, p.Repo, p.Group); err == nil && ok {
		_ = m.d.Leases.Release(ctx, lease)
	}
	if m.d.Resource != nil {
		m.d.Resource.EndGroup(p.Group)
	}

	return nil
}

// stopParkedLocked cancels the parked timer of a group that a run took over; the caller holds the lock.
func (m *Manager) stopParkedLocked(group string) {
	if t := m.parked[group]; t != nil {
		t.Stop()
		delete(m.parked, group)
	}
}
