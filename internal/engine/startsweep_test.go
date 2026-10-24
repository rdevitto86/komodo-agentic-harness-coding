package engine

import (
	"context"
	"sync/atomic"
	"testing"
)

// countSweeper counts the sweeps the engine asks for.
type countSweeper struct{ n atomic.Int32 }

// Sweep counts one call.
func (c *countSweeper) Sweep(context.Context) error {
	c.n.Add(1)
	return nil
}

// An engine start runs the sweep once, after the saved override timers are re-armed.
func TestAStartRunsTheSweepOnce(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle, withClock)
	s := &countSweeper{}
	x.e.m.d.Sweeper = s
	x.serve()

	until(t, "the sweep to run", func() bool { return s.n.Load() == 1 })
}
