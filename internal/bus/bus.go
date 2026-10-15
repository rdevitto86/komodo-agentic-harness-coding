package bus

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// DefaultBufferEvents is the queue size used when Options.BufferEvents is not positive; it matches the bus.buffer_events default.
const DefaultBufferEvents = 50

// Bus accepts events and delivers each to the subscriptions that asked for its type.
type Bus interface {
	// Publish validates the event, stamps its time and queues it for every matching subscription; it never waits for a subscriber.
	Publish(e Event) error
	// Subscribe returns a private queue of events of the given types; no types means every type.
	Subscribe(name string, types ...Type) (*Subscription, error)
	// Close stops delivery and closes every subscription's channel.
	Close() error
}

// Options holds the values the Configuration Manager hands to New.
type Options struct {
	BufferEvents int // queue size per subscription
}

// now returns the current time; tests replace it to fix the stamp.
var now = time.Now

// memBus is the in-process Bus; one mutex orders every publish, so all subscribers see one order.
type memBus struct {
	mu     sync.Mutex
	buffer int
	subs   []*Subscription
	closed bool
}

// New returns an in-process bus; the engine holds the one bus of a machine.
func New(o Options) Bus {
	if o.BufferEvents <= 0 {
		o.BufferEvents = DefaultBufferEvents
	}
	return &memBus{buffer: o.BufferEvents}
}

// Publish checks the pair and data, stamps the time and queues the event for every matching subscription.
func (b *memBus) Publish(e Event) error {
	if !validate(e) {
		return fmt.Errorf("publish %q/%q: %w", e.Type, e.Subtype, ErrUnknownEvent)
	}
	e.Time = now().UTC().Truncate(time.Second)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	b.fanOutLocked(e)
	return nil
}

// fanOutLocked queues e and then any bus.overflow events its drops caused, so every subscriber sees them in one order.
func (b *memBus) fanOutLocked(e Event) {
	pending := []Event{e}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]
		for _, s := range slices.Clone(b.subs) {
			if !s.wants(next.Type) {
				continue
			}
			select {
			case s.queue <- next:
				s.overflowing = false
			default:
				n := s.dropped.Add(1)
				// A dropped bus.overflow is counted but never announced, or a full queue would feed itself.
				if !s.overflowing && next.Subtype != (BusOverflow{}).EventKey().Subtype {
					pending = append(pending, Event{
						Type: TypeError, Subtype: BusOverflow{}.EventKey().Subtype, Time: next.Time,
						Data: &BusOverflow{Subscription: s.name, Dropped: n},
					})
				}
				s.overflowing = true
			}
		}
	}
}

// Subscribe adds a subscription for the given types, or for every type when none are given.
func (b *memBus) Subscribe(name string, types ...Type) (*Subscription, error) {
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

// Close stops delivery and closes every subscription's channel; closing twice is harmless.
func (b *memBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, s := range slices.Clone(b.subs) {
		b.removeLocked(s)
	}
	return nil
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
