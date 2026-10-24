package engine

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// The global pause's trip rule; its waits are the same as a group's.
const (
	pauseTrip   = 3                // groups that hit one error type, inside the window, that pause new session starts
	pauseWindow = 10 * time.Minute // how far back those errors may lie
)

// How a pause ended, and the names the handler gives what is not a state Reason.
const (
	byWait       = "wait"
	byPerson     = "person"
	tierSevere   = "severe"
	kindEngine   = "engine"
	maxPauseStep = 3
)

// errNote is one blocker error the global pause counts.
type errNote struct {
	reason state.Reason
	group  string
	at     time.Time
}

// errCount is how often one error type was recorded, with its tier.
type errCount struct {
	tier string
	n    int
}

// ErrorCount is one row of the error counts a status reply shows.
type ErrorCount struct {
	Type  string `json:"type"`  // the error type: a Reason, or engine for an engine failure
	Tier  string `json:"tier"`  // blocker, fault, minor, work, severe or none
	Count int    `json:"count"` // how often it was recorded since the engine started
}

// errorHandler is the one catch-all for errors: it classifies each, counts it by type, pauses new session starts when one
// type hits several groups, and stops them for good on an engine failure. It keeps no file; its events are the record.
type errorHandler struct {
	clk     Clock
	publish func(Event)
	resume  func(group string) // asks a group to perform the session starts it held back

	mu         sync.Mutex
	counts     map[string]*errCount
	recent     []errNote // blocker errors inside the window since the last resume
	paused     bool      // new session starts are held back
	stuck      bool      // the longest wait failed: only a person ends the pause
	reason     state.Reason
	step       int // the wait in force: 1, 2 or 3 for 1, 2 or 4 minutes
	until      time.Time
	lastResume time.Time
	timer      Timer
	held       []string // groups holding session starts back, oldest first
	severe     string   // what failed in the engine; empty while none has
}

// newErrorHandler returns a handler with nothing recorded.
func newErrorHandler(clk Clock, publish func(Event), resume func(string)) *errorHandler {
	return &errorHandler{clk: clk, publish: publish, resume: resume, counts: map[string]*errCount{}}
}

// record classifies the error a group's input stood for and counts it by type; for a blocker it returns how long the group
// waits before its next session start, and three groups on one type inside ten minutes pause every start.
func (h *errorHandler) record(run, group string, r state.Reason, streak int) time.Duration {
	tier := state.Classify(r)

	wait := time.Duration(0)
	if tier == state.SeverityBlocker {
		wait = groupWait(streak)
	}

	h.mu.Lock()
	h.count(string(r), string(tier))

	var trip *StartsPaused
	if tier == state.SeverityBlocker {
		trip = h.note(group, r)
	}
	h.mu.Unlock()

	h.publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "error.recorded",
		Run:     bus.ID(run),
		Group:   bus.ID(group),
		Data:    &ErrorRecorded{Reason: string(r), Tier: string(tier), WaitS: int(wait / time.Second)},
	})
	if trip != nil {
		h.publish(Event{Type: bus.TypeRunFailure, Subtype: "starts.paused", Data: trip})
	}

	return wait
}

// count adds one to a type's count; h.mu is held.
func (h *errorHandler) count(kind, tier string) {
	c := h.counts[kind]
	if c == nil {
		c = &errCount{tier: tier}
		h.counts[kind] = c
	}

	c.n++
}

// note adds a blocker error to the window and pauses the starts when the type reached the trip rule; h.mu is held.
func (h *errorHandler) note(group string, r state.Reason) *StartsPaused {
	now := h.clk.Now()
	h.recent = slices.DeleteFunc(h.recent, func(n errNote) bool { return now.Sub(n.at) >= pauseWindow })
	h.recent = append(h.recent, errNote{r, group, now})

	if h.paused {
		return nil
	}

	var groups []string
	for _, n := range h.recent {
		if n.reason == r && !slices.Contains(groups, n.group) {
			groups = append(groups, n.group)
		}
	}
	if len(groups) < pauseTrip {
		return nil
	}

	// A type that recurs inside the window after a resume takes the next wait; otherwise the waits start over.
	step := 1
	if h.step > 0 && now.Sub(h.lastResume) < pauseWindow {
		step = h.step + 1
	}

	h.paused, h.reason, h.step = true, r, min(step, maxPauseStep)
	h.stuck = step > maxPauseStep
	h.stopTimer()

	p := &StartsPaused{Reason: string(r), Groups: groups}
	if h.stuck {
		h.until = time.Time{}
		return p
	}

	wait := groupWait(h.step)
	h.until = now.Add(wait)
	h.timer = h.clk.AfterFunc(wait, h.lift)
	p.WaitS = int(wait / time.Second)

	return p
}

// lift ends a pause wait: held groups may start again, and the wait stays on record in case the type recurs.
func (h *errorHandler) lift() {
	h.mu.Lock()
	if !h.paused || h.stuck {
		h.mu.Unlock()
		return
	}

	reason, held := h.reason, h.held
	h.paused, h.held, h.recent, h.timer = false, nil, nil, nil
	h.lastResume = h.clk.Now()
	h.mu.Unlock()

	h.publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "starts.resumed",
		Data:    &StartsResumed{Reason: string(reason), By: byWait},
	})

	for _, g := range held {
		h.resume(g)
	}
}

// person ends every pause and the severe state, as a person running komodo run from a terminal does.
func (h *errorHandler) person() {
	h.mu.Lock()
	if !h.paused && h.severe == "" {
		h.mu.Unlock()
		return
	}

	reason, held := h.reason, h.held
	h.paused, h.stuck, h.severe = false, false, ""
	h.step, h.held, h.recent = 0, nil, nil
	h.stopTimer()
	h.mu.Unlock()

	h.publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "starts.resumed",
		Data:    &StartsResumed{Reason: string(reason), By: byPerson},
	})

	for _, g := range held {
		h.resume(g)
	}
}

// engineFailed records a failure of the engine itself, which stops every session start until a person runs komodo run.
func (h *errorHandler) engineFailed(where string, err error) {
	h.mu.Lock()
	h.count(kindEngine, tierSevere)

	first := h.severe == ""
	if first {
		h.severe = fmt.Sprintf("%s: %v", where, err)
	}
	h.mu.Unlock()

	if first {
		h.publish(Event{
			Type:    bus.TypeRunFailure,
			Subtype: "engine.failed",
			Data:    &EngineFailed{Where: where, Detail: err.Error()},
		})
	}
}

// stopTimer cancels the pause timer; h.mu is held.
func (h *errorHandler) stopTimer() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// allow reports whether a group may start sessions now; a group that may not is remembered and resumed when starts may go on.
func (h *errorHandler) allow(group string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.paused && h.severe == "" {
		return true
	}
	if !slices.Contains(h.held, group) {
		h.held = append(h.held, group)
	}

	return false
}

// drop forgets a held group that stopped working.
func (h *errorHandler) drop(group string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held = slices.DeleteFunc(h.held, func(g string) bool { return g == group })
}

// alert returns the one status line that says why no new session starts; empty when starts are open.
func (h *errorHandler) alert() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case h.severe != "":
		return "SEVERE: the engine failed (" + h.severe + "). " +
			"No new sessions start until a person runs komodo run from a terminal."
	case h.stuck:
		return "No new sessions start: " + string(h.reason) + " errors keep hitting several groups. " +
			"A person must run komodo run from a terminal."
	case h.paused:
		return "New sessions are paused until " + h.until.UTC().Format("15:04:05") + " UTC: " + string(h.reason) +
			" errors hit several groups."
	}

	return ""
}

// counted returns the error counts by type, most frequent first.
func (h *errorHandler) counted() []ErrorCount {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]ErrorCount, 0, len(h.counts))
	for kind, c := range h.counts {
		out = append(out, ErrorCount{Type: kind, Tier: c.tier, Count: c.n})
	}

	slices.SortFunc(out, func(a, b ErrorCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Type, b.Type))
	})

	return out
}

// startsHeld reports whether the handler holds every session start back.
func (h *errorHandler) startsHeld() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.paused || h.severe != ""
}
