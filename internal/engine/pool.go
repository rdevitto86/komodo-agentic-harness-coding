package engine

import (
	"context"
	"sync"
	"time"
)

// Pool is the active-group pool every run in the engine shares.
type Pool struct {
	mu        sync.Mutex
	size      int
	used      int
	lo, hi    int
	sessionGB float64
	clock     Clock
	cooldown  time.Duration
	lastHalve time.Time
	waiters   []chan struct{}
}

// NewPool returns a pool that starts at the ceiling of the limits.
func NewPool(l Limits, c Clock, cooldown time.Duration) *Pool {
	lo, hi := max(l.PoolMin, 1), max(l.PoolMax, 1)
	hi = max(hi, lo)

	return &Pool{
		size:      hi,
		lo:        lo,
		hi:        hi,
		sessionGB: l.SessionMemGB,
		clock:     clockOf(c),
		cooldown:  cooldown,
	}
}

// Size returns the most groups that may be active.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.size
}

// Used returns the number of active groups.
func (p *Pool) Used() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.used
}

// TryAcquire takes a slot when one is free and nobody waits for it.
func (p *Pool) TryAcquire() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.used < p.size && len(p.waiters) == 0 {
		p.used++

		return true
	}

	return false
}

// Acquire waits for a slot; it returns the context's error when the context ends first.
func (p *Pool) Acquire(ctx context.Context) error {
	p.mu.Lock()
	if p.used < p.size && len(p.waiters) == 0 {
		p.used++
		p.mu.Unlock()

		return nil
	}

	w := make(chan struct{})
	p.waiters = append(p.waiters, w)
	p.mu.Unlock()

	select {
	case <-w:
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		defer p.mu.Unlock()

		for i, x := range p.waiters {
			if x == w {
				p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)

				return ctx.Err()
			}
		}

		p.used-- // the slot was handed over as the context ended
		p.drainLocked()

		return ctx.Err()
	}
}

// Release returns a slot and hands it to the first waiter.
func (p *Pool) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.used > 0 {
		p.used--
	}

	p.drainLocked()
}

// Halve shrinks the pool after a rate-limit error; signals inside the cooldown change nothing.
func (p *Pool) Halve() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clock.Now()
	if !p.lastHalve.IsZero() && now.Sub(p.lastHalve) < p.cooldown {
		return
	}

	p.lastHalve = now
	p.size = HalvePool(p.size, p.lo)
}

// Grow enlarges the pool by one after a clean session end when free memory covers one more session.
func (p *Pool) Grow(freeMemGB float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.size = GrowPool(p.size, p.hi, freeMemGB, p.sessionGB)
	p.drainLocked()
}

// drainLocked hands free slots to waiting groups in order.
func (p *Pool) drainLocked() {
	for len(p.waiters) > 0 && p.used < p.size {
		p.used++
		close(p.waiters[0])
		p.waiters = p.waiters[1:]
	}
}

// GrowPool returns the pool size after one clean session end.
func GrowPool(size, ceiling int, freeMemGB, sessionMemGB float64) int {
	if size < ceiling && freeMemGB >= sessionMemGB {
		return size + 1
	}

	return size
}

// HalvePool returns the pool size after a rate-limit error, never below the floor.
func HalvePool(size, floor int) int { return max(size/2, floor) }
