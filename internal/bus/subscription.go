package bus

import "sync/atomic"

// Subscription is one subscriber's ordered, bounded queue.
type Subscription struct {
	Events <-chan Event // events in publish order; closed after Close

	name        string
	types       map[Type]struct{} // empty means every type
	queue       chan Event        // the writable end of Events
	bus         *memBus           // the bus that fans out to this subscription
	closed      bool              // guarded by bus.mu
	overflowing bool              // a bus.overflow was announced and the queue has not drained to half; guarded by bus.mu
	dropped     atomic.Uint64     // events lost to a full queue
}

// Close ends the subscription; later events are not queued for it.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.bus.removeLocked(s)
}

// Dropped returns how many events this subscription lost to a full queue.
func (s *Subscription) Dropped() uint64 {
	return s.dropped.Load()
}

// wants reports whether the subscription asked for events of type t.
func (s *Subscription) wants(t Type) bool {
	if len(s.types) == 0 {
		return true
	}
	_, ok := s.types[t]
	return ok
}
