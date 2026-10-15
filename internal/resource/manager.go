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

	mu    sync.Mutex // orders Observe and every change of st and saved; held while a call to another part runs
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
		w := m.st.window(k)
		w.PercentUsed, w.ResetAt, w.Sessions = r.PercentUsed, r.ResetAt, r.Sessions
		m.saved[k] = r
	}
}

// Observe applies one resource event and raises any errors it causes; it waits for any other Observe, grant or end call to finish.
func (m *Manager) Observe(ctx context.Context, e bus.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	o, keys, ok := m.st.apply(e, m.now())
	if !ok {
		return nil
	}
	res := Evaluate(m.st, m.t, o)
	return errors.Join(m.save(ctx, keys), m.deliver(ctx, o, res))
}

// save stores each changed window whose figures differ from the last accepted record; a failure is reported and the window stays in memory.
func (m *Manager) save(ctx context.Context, keys []WindowKey) error {
	if m.d.State == nil {
		return nil
	}
	var errs []error
	for _, k := range keys {
		w := m.st.Windows[k]
		r := WindowRecord{PercentUsed: w.PercentUsed, ResetAt: w.ResetAt, Sessions: w.Sessions}
		if old, had := m.saved[k]; had && sameRecord(old, r) {
			continue
		}
		if err := m.d.State.SaveWindow(ctx, k, r); err != nil {
			err = fmt.Errorf("save window %s of %s: %w", k.Window, k.Link, err)
			m.report(err)
			errs = append(errs, err)
			continue
		}
		m.saved[k] = r
	}
	return errors.Join(errs...)
}

// deliver throws each new error, then sends each pause order whose error was thrown, then each nudge.
// A flag is set only after its call succeeds, and a failed call is reported and tried again on the next event.
func (m *Manager) deliver(ctx context.Context, o Observation, res Result) error {
	var errs []error
	fail := func(err error) {
		m.report(err)
		errs = append(errs, err)
	}
	for i, re := range res.Errors {
		if err := m.d.Errors.ThrowResourceError(ctx, re); err != nil {
			fail(fmt.Errorf("throw %s: %w", re.Kind, err))
			continue
		}
		m.st.markRaised(o, res.keys[i], re)
	}
	for _, p := range res.Pauses {
		if !m.st.errorRaised(p) {
			continue
		}
		if err := m.d.Runtime.PauseGroups(ctx, p); err != nil {
			fail(fmt.Errorf("pause %s: %w", p.Reason, err))
			continue
		}
		m.st.markPaused(p)
	}
	for _, n := range res.Nudges {
		if err := m.d.Runtime.NudgeGroups(ctx, n); err != nil {
			fail(fmt.Errorf("nudge groups: %w", err))
			continue
		}
		m.st.markNudged(n)
	}
	return errors.Join(errs...)
}

// GrantGroupTokens raises a group's cap by one limits.group_tokens and clears the group's nudge and cap flags.
// It waits for an Observe in progress, so a grant is never overwritten by the delivery it raced; the Runtime Manager must not call it from inside a pause or nudge.
func (m *Manager) GrantGroupTokens(group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &m.st.Spend
	s.CapGrants[group]++
	delete(s.GroupNudged, group)
	delete(s.GroupCapped, group)
	delete(s.GroupCapRaised, group)
}

// EndRun drops a finished run's totals, flags and group list.
func (m *Manager) EndRun(run string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.forgetRun(run)
}

// EndGroup drops a finished group's totals and flags and removes it from every group list.
func (m *Manager) EndGroup(group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.forgetGroup(group)
}

// report sends an ordinary error to the Error Handler.
func (m *Manager) report(err error) {
	if m.d.Errors == nil {
		return
	}
	m.d.Errors.Report(fault.Source{Component: "resource"}, string(format.Internal), fault.KindInternal, err)
}
