package resource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// subscriberName is the name the Resource Manager gives its bus subscription.
const subscriberName = "resource"

// errMalformed marks an event whose data the Resource Manager cannot use.
var errMalformed = errors.New("malformed resource event")

// Manager follows resource events, keeps the running totals and orders pauses and nudges.
type Manager struct {
	t   Thresholds
	d   Deps
	now func() time.Time

	obsMu sync.Mutex // orders Observe calls so one event finishes before the next begins
	mu    sync.Mutex // guards st and saved; never held across a call to another part
	st    *State
	saved map[WindowKey]WindowRecord // what the State Manager last accepted for each window
}

// New builds a Resource Manager from its thresholds and the other parts it calls.
func New(t Thresholds, d Deps) *Manager {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{t: t, d: d, now: now, st: NewState(), saved: map[WindowKey]WindowRecord{}}
}

// Run loads the stored windows, subscribes to resource events and serves them until ctx ends or the subscription closes.
func (m *Manager) Run(ctx context.Context) error {
	m.load(ctx)
	sub, err := m.d.Bus.Subscribe(subscriberName, bus.TypeResource)
	if err != nil {
		return fmt.Errorf("subscribe to resource events: %w", err)
	}
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub.Events:
			if !ok {
				return nil
			}
			// A failed observation is already reported or retried by the next event; the loop goes on.
			_ = m.Observe(ctx, e)
		}
	}
}

// load reads every stored window whose reset time is still ahead; a failed read is reported and the totals start empty.
func (m *Manager) load(ctx context.Context) {
	if m.d.State == nil {
		return
	}
	recs, err := m.d.State.LoadWindows(ctx)
	if err != nil {
		m.report(fmt.Errorf("load window records: %w", err))
		return
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range recs {
		if !r.ResetAt.After(now) {
			continue
		}
		m.st.Windows[k] = &WindowState{PercentUsed: r.PercentUsed, ResetAt: r.ResetAt, Sessions: r.Sessions, Reading: true}
		m.saved[k] = r
	}
}

// Observe applies one resource event and raises any errors it causes.
func (m *Manager) Observe(ctx context.Context, e bus.Event) error {
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	if e.Type != bus.TypeResource {
		return nil
	}
	if err := check(e); err != nil {
		if !errors.Is(err, errUnknownSubtype) {
			m.report(err)
			return err
		}
		slog.Info("ignored resource event", "subtype", string(e.Subtype))
		return nil
	}
	o, res, dirty := m.step(e)
	return errors.Join(m.save(ctx, dirty), m.deliver(ctx, o, res))
}

// step applies a checked event to the totals and evaluates the rules, all under the state lock.
func (m *Manager) step(e bus.Event) (Observation, Result, map[WindowKey]WindowRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, keys, ok := m.st.apply(e, m.now())
	if !ok {
		return o, Result{}, nil
	}
	dirty := map[WindowKey]WindowRecord{}
	for _, k := range keys {
		if w := m.st.Windows[k]; w != nil && w.Reading {
			dirty[k] = WindowRecord{PercentUsed: w.PercentUsed, ResetAt: w.ResetAt, Sessions: w.Sessions}
		}
	}
	return o, Evaluate(m.st, m.t, o), dirty
}

// save stores each window whose figures differ from the last accepted record; a failure is reported and the window stays in memory.
func (m *Manager) save(ctx context.Context, dirty map[WindowKey]WindowRecord) error {
	if m.d.State == nil {
		return nil
	}
	var errs []error
	for k, r := range dirty {
		m.mu.Lock()
		old, had := m.saved[k]
		m.mu.Unlock()
		if had && sameRecord(old, r) {
			continue
		}
		if err := m.d.State.SaveWindow(ctx, k, r); err != nil {
			err = fmt.Errorf("save window %s of %s: %w", k.Window, k.Link, err)
			m.report(err)
			errs = append(errs, err)
			continue
		}
		m.mu.Lock()
		m.saved[k] = r
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// deliver throws each new error, then sends each pause order whose error was thrown, then each nudge; a flag is set only after its call succeeds.
func (m *Manager) deliver(ctx context.Context, o Observation, res Result) error {
	var errs []error
	for _, re := range res.Errors {
		if err := m.d.Errors.ThrowResourceError(ctx, re); err != nil {
			errs = append(errs, err)
			continue
		}
		m.mu.Lock()
		m.st.markRaised(o, re)
		m.mu.Unlock()
	}
	for _, p := range res.Pauses {
		m.mu.Lock()
		ready := m.st.errorRaised(o, p)
		m.mu.Unlock()
		if !ready {
			continue
		}
		if err := m.d.Runtime.PauseGroups(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("pause %s: %w", p.Reason, err))
			continue
		}
		m.mu.Lock()
		m.st.markPaused(o, p)
		m.mu.Unlock()
	}
	for _, n := range res.Nudges {
		if err := m.d.Runtime.NudgeGroups(ctx, n); err != nil {
			errs = append(errs, fmt.Errorf("nudge groups: %w", err))
			continue
		}
		m.mu.Lock()
		m.st.markNudged(n)
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// GrantGroupTokens raises a group's cap by one limits.group_tokens and clears the group's nudge and cap flags.
func (m *Manager) GrantGroupTokens(group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &m.st.Spend
	s.CapGrants[group]++
	delete(s.GroupNudged, group)
	delete(s.GroupCapped, group)
	delete(s.GroupCapRaised, group)
}

// report sends an ordinary error to the Error Handler.
func (m *Manager) report(err error) {
	if m.d.Errors == nil {
		return
	}
	m.d.Errors.Report(fault.Source{Component: "resource"}, string(format.Internal), fault.KindInternal, err)
}
