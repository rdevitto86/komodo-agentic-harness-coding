package commit

import (
	"context"
	"sync"
)

// EpicGates holds each epic's landing turn in memory; one engine per machine lands, so no file is
// needed. The zero value is ready to use.
type EpicGates struct {
	mu    sync.Mutex
	epics map[string]*epicGate
}

// epicGate is one epic's turn, queue and pause.
type epicGate struct {
	held     bool      // a group holds the turn
	seen     bool      // some group has held a turn since the engine started
	pausedBy string    // group whose failed recheck paused the epic; empty when running
	queue    []*waiter // groups waiting, first come first served
}

// waiter is one group waiting for its turn.
type waiter struct {
	group    string
	anyPause bool // admitted even while another group's failed recheck holds the epic
	ready    chan struct{}
	turn     *Turn // set under the lock when the turn is granted
}

// Turn is one group's hold on its epic's landing turn.
type Turn struct {
	gates  *EpicGates
	epic   string
	group  string
	first  bool
	paused bool
	done   bool
}

// Enter waits for the epic's turn, first come first served; a paused epic admits only the group
// that paused it.
func (g *EpicGates) Enter(ctx context.Context, epic, group string) (*Turn, error) {
	return g.enter(ctx, epic, &waiter{group: group, ready: make(chan struct{})})
}

// Lock waits for the epic's turn like Enter but is admitted even while the epic is paused; it only
// lets the caller edit the epic's landing worktree, so it never changes the pause.
func (g *EpicGates) Lock(ctx context.Context, epic string) (*Turn, error) {
	return g.enter(ctx, epic, &waiter{anyPause: true, ready: make(chan struct{})})
}

// gate returns the epic's gate, creating it; the caller holds the lock.
func (g *EpicGates) gate(epic string) *epicGate {
	if g.epics == nil {
		g.epics = map[string]*epicGate{}
	}

	e := g.epics[epic]
	if e == nil {
		e = &epicGate{}
		g.epics[epic] = e
	}

	return e
}

// grant gives the free turn to group and returns it; the caller holds the lock.
// A turn taken only to edit the landing worktree is not a landing: it neither counts as the epic's
// first turn nor carries a pause.
func (g *EpicGates) grant(epic string, e *epicGate, w *waiter) *Turn {
	t := &Turn{gates: g, epic: epic, group: w.group}
	if !w.anyPause {
		t.first, t.paused = !e.seen, e.pausedBy != ""
		e.seen = true
	}

	e.held = true

	return t
}

// dispatch hands a free turn to the next admissible waiter; the caller holds the lock.
// A paused epic admits only the group that paused it, wherever it stands in the queue.
func (g *EpicGates) dispatch(epic string, e *epicGate) {
	if e.held {
		return
	}

	for i, w := range e.queue {
		if e.pausedBy != "" && w.group != e.pausedBy && !w.anyPause {
			continue
		}

		e.queue = append(e.queue[:i], e.queue[i+1:]...)
		w.turn = g.grant(epic, e, w)
		close(w.ready)

		return
	}
}

// enter admits w at once when it may, and otherwise queues it until its turn or the end of ctx.
func (g *EpicGates) enter(ctx context.Context, epic string, w *waiter) (*Turn, error) {
	g.mu.Lock()
	e := g.gate(epic)

	free := !e.held &&
		((w.anyPause && (len(e.queue) == 0 || e.pausedBy != "")) ||
			(!w.anyPause && ((e.pausedBy == "" && len(e.queue) == 0) || e.pausedBy == w.group)))

	if free {
		t := g.grant(epic, e, w)
		g.mu.Unlock()

		return t, nil
	}

	e.queue = append(e.queue, w)
	g.mu.Unlock()

	select {
	case <-w.ready:
		return w.turn, nil
	case <-ctx.Done():
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if w.turn != nil {
		e.held = false
		g.dispatch(epic, e)

		return nil, ctx.Err()
	}

	for i, q := range e.queue {
		if q == w {
			e.queue = append(e.queue[:i], e.queue[i+1:]...)

			break
		}
	}

	return nil, ctx.Err()
}

// Paused reports whether a failed recheck holds the epic.
func (g *EpicGates) Paused(epic string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gate(epic).pausedBy != ""
}

// queued returns how many groups wait for the epic's turn.
func (g *EpicGates) queued(epic string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.gate(epic).queue)
}

// Finish frees the turn: a failed recheck pauses the epic under this group, a passing one clears
// the pause.
func (t *Turn) Finish(pass bool) {
	t.free(func(e *epicGate) {
		if pass {
			e.pausedBy = ""
		} else {
			e.pausedBy = t.group
		}
	})
}

// Release frees the turn without a recheck result and leaves the pause as it was.
func (t *Turn) Release() { t.free(func(*epicGate) {}) }

// First reports whether no group had held a turn on this epic since the engine started.
func (t *Turn) First() bool { return t.first }

// Paused reports whether the epic was paused when this turn began.
func (t *Turn) Paused() bool { return t.paused }

// free ends the turn once; set runs under the lock before the next group is admitted.
func (t *Turn) free(set func(e *epicGate)) {
	t.gates.mu.Lock()
	defer t.gates.mu.Unlock()

	if t.done {
		return
	}

	t.done = true
	e := t.gates.gate(t.epic)
	set(e)
	e.held = false
	t.gates.dispatch(t.epic, e)
}
