package bus

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// DefaultBufferEvents is the queue size used when Options.BufferEvents is not positive.
const DefaultBufferEvents = 50

// busOverflowKey is the pair of the event the bus publishes when a queue is full.
var busOverflowKey = BusOverflow{}.EventKey()

// Bus accepts events and delivers each to the subscriptions that asked for its type.
type Bus interface {
	// Publish validates the event, stamps its time and queues it for every matching subscription; it never waits for a subscriber.
	// Publish takes ownership of e.Data: the publisher and every subscriber treat it as read-only afterwards.
	Publish(e Event) error
	// Subscribe returns a private queue of events of the given types; no types means every type.
	Subscribe(name string, types ...Type) (*Subscription, error)
	// Seal refuses every later Publish with ErrBusSealed; queued events still reach their subscribers.
	Seal()
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
	sealed bool
}

// New returns an in-process bus; the engine holds the one bus of a machine.
func New(o Options) Bus {
	if o.BufferEvents <= 0 {
		o.BufferEvents = DefaultBufferEvents
	}
	return &memBus{buffer: o.BufferEvents}
}

// Publish checks the pair and data, then under the bus lock stamps the time and queues the event for every matching subscription.
// The data pointer is shared by all subscribers, so neither the publisher nor a subscriber modifies it after the call.
func (b *memBus) Publish(e Event) error {
	if !validate(e) {
		return fmt.Errorf("publish %q/%q: %w", e.Type, e.Subtype, ErrUnknownEvent)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	if b.sealed {
		return ErrBusSealed
	}
	e.Time = now().UTC().Truncate(time.Second)
	b.fanOutLocked(e)
	return nil
}

// fanOutLocked queues e and then any bus.overflow events its drops caused, so every subscriber sees them in one order.
func (b *memBus) fanOutLocked(e Event) {
	pending := []Event{e}
	for len(pending) > 0 {
		next := pending[0]
		pending = pending[1:]
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

// Close stops delivery and closes every subscription's channel; closing twice is harmless.
func (b *memBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, s := range b.subs {
		s.closed = true
		close(s.queue)
	}
	b.subs = nil
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
