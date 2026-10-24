package bus

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// DefaultBufferEvents is the queue size used when Options.BufferEvents is not positive.
const DefaultBufferEvents = 50

// busOverflowKey is the pair of the event the bus publishes when a queue is full.
var busOverflowKey = BusOverflow{}.EventKey()

// busStoreFailedKey is the pair of the event the bus publishes when an append to the event store fails.
var busStoreFailedKey = BusStoreFailed{}.EventKey()

// Bus accepts events and delivers each to the subscriptions that asked for its type.
type Bus interface {
	// Publish validates the event, stamps its time, appends it to the event store and queues it for every
	// matching subscription; it never waits for a subscriber. Publish takes ownership of e.Data: the publisher and
	// every subscriber treat it as read-only afterwards.
	Publish(e Event) error
	// Subscribe returns a private queue of events of the given types; no types means every type.
	Subscribe(name string, types ...Type) (*Subscription, error)
	// Seal refuses every later Publish with ErrBusSealed; queued events still reach their subscribers.
	Seal()
	// Close stops delivery, closes every subscription's channel and the event logs, and waits for a running trim.
	Close() error
}

// New returns an in-process bus; the engine holds the one bus of a machine.
func New(o Options) Bus {
	if o.BufferEvents <= 0 {
		o.BufferEvents = DefaultBufferEvents
	}

	b := &memBus{
		buffer: o.BufferEvents, dir: o.EventsDir, mask: o.Mask, retention: o.RetentionDays, runOpen: o.RunOpen, goFn: o.Go,
		runDir: map[ID]string{}, logs: map[string]*openLog{}, missed: map[string]int{}, failed: map[string]bool{},
		lastTrim: map[string]time.Time{},
	}

	if b.dir != "" {
		// A runs.json that cannot be read starts the map empty and is reported; a corrupt one is moved aside first.
		path := filepath.Join(b.dir, runsFile)
		m, note, err := loadRuns(path)
		if err == nil {
			b.runDir = m
		} else {
			note = err.Error()
		}
		if note != "" {
			b.reportStore("", path, note)
		}
	}
	return b
}

// Options holds the values the Configuration Manager hands to New.
type Options struct {
	// queue size per subscription
	BufferEvents int
	// the machine events folder, ~/.komodo/events; empty turns the event store off
	EventsDir string
	// days an event log is kept; a value below 1 means 30
	RetentionDays int
	// reports whether a run is open, so its log is never trimmed; nil counts every run as open
	RunOpen func(ID) bool
	// starts a function on a goroutine the engine guards against panics; nil runs it before Publish returns
	Go func(func())
	// hides secrets in a line before it is written; nil writes it as is
	Mask func(string) string
}

// now returns the current time; tests replace it to fix the stamp.
var now = time.Now

// memBus is the in-process Bus; one mutex orders every publish, so the event log and all subscribers see one order.
type memBus struct {
	mu     sync.Mutex
	buffer int
	subs   []*Subscription
	closed bool
	sealed bool

	// The fields below are the event store; all are guarded by mu.
	dir       string               // the machine events folder; empty when the store is off
	mask      func(string) string  // hides secrets in a line before it is written
	retention int                  // days an event log is kept
	runOpen   func(ID) bool        // reports whether a run is open
	runDir    map[ID]string        // the events folder of each run
	logs      map[string]*openLog  // open logs by path, at most maxOpenLogs
	useSeq    uint64               // counts log writes, to find the least recently written log
	follow    followUp             // work the current publish leaves for after the lock
	saveMu    sync.Mutex           // orders the runs.json read-merge-write; taken without mu
	missed    map[string]int       // events lost to failed appends, by log, not yet marked
	failed    map[string]bool      // logs whose failure burst was announced and has not ended
	lastTrim  map[string]time.Time // when each events folder was last trimmed
	goFn      func(func())         // starts a trim; nil runs it inline
	trims     sync.WaitGroup       // running background trims
}

// Publish checks the pair and data, then under the bus lock stamps the time, appends the event to
// the event store and queues it for every matching subscription. The data pointer is shared by all subscribers,
// so neither the publisher nor a subscriber modifies it after the call.
func (b *memBus) Publish(e Event) error {
	if !validate(e) {
		return fmt.Errorf("publish %q/%q: %w", e.Type, e.Subtype, ErrUnknownEvent)
	}
	fu, err := b.publish(e)
	b.finish(fu)
	return err
}

// Subscribe adds a subscription for the given types, or for every type when none are given.
func (b *memBus) Subscribe(name string, types ...Type) (*Subscription, error) {
	for _, t := range types {
		if !slices.Contains(allTypes, t) {
			return nil, fmt.Errorf("subscribe %q to %q: %w", name, t, ErrUnknownEvent)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrBusClosed
	}

	q := make(chan Event, b.buffer)
	s := &Subscription{Events: q, name: name, queue: q, bus: b}
	if len(types) > 0 {
		s.types = make(map[Type]struct{}, len(types))
		for _, t := range types {
			s.types[t] = struct{}{}
		}
	}
	b.subs = append(b.subs, s)
	return s, nil
}

// Seal makes every later Publish fail with ErrBusSealed; subscriptions keep draining what is queued.
func (b *memBus) Seal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = true
}

// Close stops delivery, closes every subscription's channel and every open log,
// and waits for a running trim; closing twice is harmless.
func (b *memBus) Close() error {
	b.mu.Lock()
	b.closed = true

	for _, s := range b.subs {
		s.closed = true
		close(s.queue)
	}

	b.subs = nil
	for p := range b.logs {
		b.closeLogLocked(p)
	}

	b.mu.Unlock()
	b.trims.Wait()
	return nil
}

// publish stamps, stores and queues e under the bus lock and returns the work left for after it.
func (b *memBus) publish(e Event) (followUp, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return followUp{}, ErrBusClosed
	}
	if b.sealed {
		return followUp{}, ErrBusSealed
	}

	e.Time = now().UTC().Truncate(time.Second)
	b.fanOutLocked(e)
	fu := b.follow
	b.follow = followUp{}
	return fu, nil
}

// fanOutLocked stores and queues e and then any bus.overflow or bus.store_failed events it caused,
// so the log and every subscriber see one order.
func (b *memBus) fanOutLocked(e Event) {
	pending := []Event{e}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]
		pending = append(pending, b.storeLocked(next)...)
		for _, s := range b.subs {
			if !s.wants(next.Type) {
				continue
			}
			select {
			case s.queue <- next:
				// A burst ends once the queue is back at half its capacity or below; a one-slot queue ends it on any accept.
				if s.overflowing && len(s.queue) <= max(cap(s.queue)/2, 1) {
					s.overflowing = false
				}
			default:
				n := s.dropped.Add(1)
				// Every drop that starts a burst is announced, except a dropped bus.overflow, which is only counted.
				if !s.overflowing && (Key{next.Type, next.Subtype}) != busOverflowKey {
					s.overflowing = true
					pending = append(pending, Event{
						Type: busOverflowKey.Type, Subtype: busOverflowKey.Subtype, Time: next.Time,
						Data: &BusOverflow{Subscription: s.name, Dropped: n},
					})
				}
			}
		}
	}
}

// removeLocked drops s from the fan-out list and closes its channel once.
func (b *memBus) removeLocked(s *Subscription) {
	if s.closed {
		return
	}
	s.closed = true
	b.subs = slices.DeleteFunc(b.subs, func(x *Subscription) bool { return x == s })
	close(s.queue)
}
