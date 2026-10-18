package engine

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// FakeClock is a clock a test moves by hand.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// NewFakeClock returns a clock set to t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc arms a timer that fires when the clock passes d.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// Pending returns how many timers wait.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// Advance moves the clock and fires every timer that came due, in time order.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.done && !t.at.After(c.now) {
			t.done = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}

type fakeTimer struct {
	c    *FakeClock
	at   time.Time
	f    func()
	done bool
}

// Stop cancels the timer.
func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.done
	t.done = true
	return was
}

func TestFakeClockFiresTimersInOrder(t *testing.T) {
	c := NewFakeClock(t0)
	var got []int
	c.AfterFunc(2*time.Minute, func() { got = append(got, 2) })
	c.AfterFunc(time.Minute, func() { got = append(got, 1) })
	s := c.AfterFunc(time.Minute, func() { got = append(got, 9) })
	s.Stop()
	c.Advance(3 * time.Minute)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("fired %v", got)
	}
}
