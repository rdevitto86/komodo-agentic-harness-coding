package engine

import (
	"slices"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// The breaker's trip rule and probe timing.
const (
	breakerTrip       = 3                // panic-tier faults of one reason, on different groups, that open the breaker
	breakerWindow     = 10 * time.Minute // how far back those faults may lie
	breakerProbeEvery = 5 * time.Minute  // how long an open breaker waits before it lets one session start
)

// BreakerOpened is the data of run-failure / breaker.opened: Komodo itself keeps breaking, so no new session starts.
type BreakerOpened struct {
	Reason string   `json:"reason"` // the panic-tier reason that tripped the breaker
	Groups []string `json:"groups"` // the groups that hit it
}

// EventKey binds BreakerOpened to run-failure / breaker.opened.
func (BreakerOpened) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunFailure, Subtype: "breaker.opened"}
}

// BreakerClosed is the data of run-status / breaker.closed: a probe session ended without a panic-tier fault.
type BreakerClosed struct {
	Reason string `json:"reason"` // the reason the breaker had opened for
}

// EventKey binds BreakerClosed to run-status / breaker.closed.
func (BreakerClosed) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "breaker.closed"}
}

// breakerFault is one panic-tier fault the breaker counts.
type breakerFault struct {
	reason state.Reason
	group  string
	at     time.Time
}

// breaker is the engine-wide circuit breaker: it holds back new sessions while Komodo itself keeps breaking. It keeps no
// state file; after an engine restart it starts closed, and its events are the record.
type breaker struct {
	clk     Clock
	publish func(Event)
	resume  func(group string) // asks a group to perform the session starts it held back

	mu      sync.Mutex
	faults  []breakerFault
	open    bool
	reason  state.Reason
	probing bool
	probe   string // the group whose session start was let through
	next    time.Time
	timer   Timer
	held    []string // groups holding session starts back, oldest first
}

// newBreaker returns a closed breaker.
func newBreaker(clk Clock, publish func(Event), resume func(string)) *breaker {
	return &breaker{clk: clk, publish: publish, resume: resume}
}

// note counts a panic-tier fault of a group; three of one reason on three groups inside ten minutes open the breaker, and
// a fault of the probe's group keeps an open breaker open for another wait.
func (b *breaker) note(group string, reason state.Reason) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clk.Now()
	b.faults = slices.DeleteFunc(b.faults, func(f breakerFault) bool { return now.Sub(f.at) >= breakerWindow })
	b.faults = append(b.faults, breakerFault{reason, group, now})

	if b.open {
		if b.probing && b.probe == group {
			b.probing = false
			b.wait(now)
		}
		return
	}

	var groups []string
	for _, f := range b.faults {
		if f.reason == reason && !slices.Contains(groups, f.group) {
			groups = append(groups, f.group)
		}
	}
	if len(groups) < breakerTrip {
		return
	}

	b.open, b.reason, b.probing = true, reason, false
	b.wait(now)
	b.publish(Event{
		Type:    bus.TypeRunFailure,
		Subtype: "breaker.opened",
		Data:    &BreakerOpened{Reason: string(reason), Groups: groups},
	})
}

// wait sets the next probe one wait from now and arms the timer that asks a held group to start it.
func (b *breaker) wait(now time.Time) {
	b.next = now.Add(breakerProbeEvery)
	if b.timer != nil {
		b.timer.Stop()
	}

	b.timer = b.clk.AfterFunc(breakerProbeEvery, b.tick)
}

// tick lets one held group try a session start as the probe and arms the next turn, which a started probe cancels or
// replaces; with none held, the next group to ask becomes the probe.
func (b *breaker) tick() {
	b.mu.Lock()
	if !b.open || b.probing || len(b.held) == 0 {
		b.mu.Unlock()
		return
	}

	g := b.held[0]
	b.held = b.held[1:]
	b.timer = b.clk.AfterFunc(breakerProbeEvery, b.tick)
	b.mu.Unlock()
	b.resume(g)
}

// allow reports whether a group may start sessions now; a group that may not is remembered, and resumed once the breaker
// closes or its turn as the probe comes.
func (b *breaker) allow(group string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.open {
		return true
	}
	if b.probing && b.probe == group {
		return true
	}
	if !b.probing && !b.clk.Now().Before(b.next) {
		b.probing, b.probe = true, group
		b.held = slices.DeleteFunc(b.held, func(g string) bool { return g == group })
		return true
	}
	if !slices.Contains(b.held, group) {
		b.held = append(b.held, group)
	}

	return false
}

// ended tells the breaker a group's session ended or the group stopped working; if that group was the probe and no
// panic-tier fault came, the breaker closes and every held group resumes.
func (b *breaker) ended(group string) {
	b.mu.Lock()
	if !b.open || !b.probing || b.probe != group {
		b.mu.Unlock()
		return
	}

	reason := b.reason
	held := b.held
	b.open, b.probing, b.held, b.faults = false, false, nil, nil
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()

	b.publish(Event{Type: bus.TypeRunStatus, Subtype: "breaker.closed", Data: &BreakerClosed{Reason: string(reason)}})

	for _, g := range held {
		b.resume(g)
	}
}

// drop forgets a held group that stopped working, so it does not spend a probe turn.
func (b *breaker) drop(group string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held = slices.DeleteFunc(b.held, func(g string) bool { return g == group })
}

// alert returns the one status line that says the breaker is open; empty when it is closed.
func (b *breaker) alert() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.open {
		return ""
	}

	return "No new sessions start: " + string(b.reason) + " faults hit several groups. " +
		"One probe session runs every 5 minutes."
}

// isOpen reports whether the breaker holds sessions back.
func (b *breaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}
