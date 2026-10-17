package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// queue is the schedule queue loop's view of schedule.json.
type queue struct {
	e      *Engine
	kick   chan struct{}
	mu     sync.Mutex
	firing map[string]bool // run ids whose entry was fired and not yet removed
}

// newID returns a version 7 UUID: 48 bits of milliseconds, then random bits.
func newID(now time.Time) string {
	var b [16]byte

	ms := uint64(now.UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (8 * (5 - i)))
	}

	_, _ = rand.Read(b[6:])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// load returns the queued entries, oldest start first; an engine with no State Manager has an empty
// queue.
func (q *queue) load() ([]state.ScheduleEntry, int, error) {
	if q.e.d.Machine == nil {
		return nil, 0, nil
	}

	rec, err := state.LoadMachine[state.ScheduleBody](q.e.d.Machine, state.KindSchedule)
	if errors.Is(err, state.ErrNotFound) {
		return nil, 0, nil
	}

	if err != nil {
		return nil, 0, err
	}

	es := slices.Clone(rec.Body.Entries)
	slices.SortStableFunc(es, func(a, b state.ScheduleEntry) int { return a.At.Compare(b.At) })

	return es, rec.Header.Rev, nil
}

// edit rewrites the queue through change and wakes the loop; a lost write race is retried.
func (q *queue) edit(change func([]state.ScheduleEntry) []state.ScheduleEntry) error {
	if q.e.d.Machine == nil {
		return fmt.Errorf("the engine has no state to hold a schedule")
	}

	var err error

	for range 5 {
		var (
			es  []state.ScheduleEntry
			rev int
		)
		if es, rev, err = q.load(); err != nil {
			return err
		}

		next := change(slices.Clone(es))
		if next == nil {
			next = []state.ScheduleEntry{}
		}

		if _, err = state.SaveMachine(
			q.e.d.Machine,
			state.KindSchedule,
			state.ScheduleBody{Entries: next},
			rev,
		); err == nil {
			q.wake()
			q.e.checkExit()

			return nil
		}
	}

	return fmt.Errorf("write the schedule queue: %w", err)
}

// wake makes the loop read the queue again.
func (q *queue) wake() {
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

// add queues a run for its start time and returns the entry.
func (q *queue) add(rs bus.RunStart, target string, at time.Time) (state.ScheduleEntry, error) {
	now := q.e.clk.Now()
	ent := state.ScheduleEntry{
		Target:   target,
		Root:     rs.Root,
		RunID:    newID(now),
		At:       at,
		QueuedAt: now.UTC().Truncate(time.Second),
		Start:    rs,
	}
	err := q.edit(func(es []state.ScheduleEntry) []state.ScheduleEntry { return append(es, ent) })

	return ent, err
}

// remove drops the entries a target or run id names and returns them.
func (q *queue) remove(target string) ([]state.ScheduleEntry, error) {
	var gone []state.ScheduleEntry

	err := q.edit(func(es []state.ScheduleEntry) []state.ScheduleEntry {
		gone = nil

		return slices.DeleteFunc(es, func(e state.ScheduleEntry) bool {
			hit := e.RunID == target || e.Target == target
			if hit {
				gone = append(gone, e)
			}

			return hit
		})
	})

	return gone, err
}

// length returns the number of queued entries.
func (q *queue) length() int {
	es, _, err := q.load()
	if err != nil {
		return 0
	}

	return len(es)
}

// loop starts every overdue entry at once and again at each wake, and arms one timer for the
// earliest entry in the future.
func (q *queue) loop(ctx context.Context) {
	var timer Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		es, _, err := q.load()
		if err != nil {
			q.e.m.reportErr("", "", fmt.Errorf("read the schedule queue: %w", err))
		}

		now := q.e.clk.Now()

		var next time.Time

		for _, ent := range es {
			if !ent.At.After(now) {
				q.fire(ctx, ent)

				continue
			}

			if next.IsZero() {
				next = ent.At
			}
		}

		if timer != nil {
			timer.Stop()
			timer = nil
		}

		if !next.IsZero() {
			timer = q.e.clk.AfterFunc(next.Sub(now), q.wake)
		}

		select {
		case <-q.kick:
		case <-ctx.Done():
			return
		}
	}
}

// fire publishes the run-start an entry holds, then removes the entry once the run's first group
// record is saved.
func (q *queue) fire(ctx context.Context, ent state.ScheduleEntry) {
	q.mu.Lock()
	if q.firing[ent.RunID] {
		q.mu.Unlock()

		return
	}

	q.firing[ent.RunID] = true
	q.mu.Unlock()
	q.e.m.spawn(func() {
		defer func() {
			q.mu.Lock()
			delete(q.firing, ent.RunID)
			q.mu.Unlock()
		}()

		start := ent.Start
		start.Request = bus.ID("sched-" + ent.RunID)

		wait := q.e.m.Expect(string(start.Request))
		if err := q.e.d.Bus.Publish(Event{
			Type: bus.TypeRunStart,
			Run:  bus.ID(ent.RunID),
			Data: &start,
		}); err != nil {
			q.e.m.reportErr(ent.RunID, "", fmt.Errorf("start queued run %s: %w", ent.Target, err))

			return
		}

		select {
		case err := <-wait:
			if err == nil {
				select {
				case <-q.e.m.firstSaved(ent.RunID):
				case <-ctx.Done():
					return
				}
			}
		case <-ctx.Done():
			return
		}

		if _, err := q.remove(ent.RunID); err != nil {
			q.e.m.reportErr(ent.RunID, "", err)
		}
	})
}
