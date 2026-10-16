package engine

import "time"

// Timer is a pending call that can be cancelled.
type Timer interface {
	// Stop cancels the call and reports whether it was still pending.
	Stop() bool
}

// Clock is the time source of the engine; tests replace it.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f once, after d, on its own goroutine.
	AfterFunc(d time.Duration, f func()) Timer
}

// realClock is the wall clock.
type realClock struct{}

// Now returns the current time.
func (realClock) Now() time.Time { return time.Now() }

// AfterFunc calls f after d.
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// clockOf returns c, or the wall clock when c is nil.
func clockOf(c Clock) Clock {
	if c == nil {
		return realClock{}
	}
	return c
}
