package commit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitQueued blocks until n groups wait on the epic.
func waitQueued(t *testing.T, g *EpicGates, epic string, n int) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if g.queued(epic) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue never reached %d (now %d)", n, g.queued(epic))
}

// Two groups of one epic land one after the other, in the order they arrived.
func TestGatesFirstComeFirstServed(t *testing.T) {
	g := &EpicGates{}
	ctx := context.Background()
	a, err := g.Enter(ctx, "ep-1", "a")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	for i, name := range []string{"b", "c"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			turn, err := g.Enter(ctx, "ep-1", name)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			turn.Finish(true)
		}(name)
		waitQueued(t, g, "ep-1", i+1)
	}
	mu.Lock()
	if len(order) != 0 {
		t.Fatalf("admitted while a held the turn: %v", order)
	}
	mu.Unlock()
	a.Finish(true)
	wg.Wait()
	if len(order) != 2 || order[0] != "b" || order[1] != "c" {
		t.Fatalf("order = %v", order)
	}
}

// Other epics are never held up.
func TestGatesEpicsIndependent(t *testing.T) {
	g := &EpicGates{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, _ := g.Enter(ctx, "ep-1", "a")
	b, err := g.Enter(ctx, "ep-2", "b")
	if err != nil {
		t.Fatalf("ep-2 blocked by ep-1: %v", err)
	}
	a.Release()
	b.Release()
}

// A paused epic admits only the group that paused it, ahead of the queue, and a pass clears the pause.
func TestGatesPauseAdmitsOnlyPauser(t *testing.T) {
	g := &EpicGates{}
	ctx := context.Background()
	a, _ := g.Enter(ctx, "ep-1", "a")
	if !a.First() || a.Paused() {
		t.Fatalf("first = %v paused = %v", a.First(), a.Paused())
	}
	got := make(chan *Turn, 1)
	go func() { turn, _ := g.Enter(ctx, "ep-1", "b"); got <- turn }()
	waitQueued(t, g, "ep-1", 1)
	a.Finish(false)
	select {
	case <-got:
		t.Fatal("b admitted while the epic was paused")
	case <-time.After(150 * time.Millisecond):
	}
	a2, err := g.Enter(ctx, "ep-1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if a2.First() || !a2.Paused() {
		t.Fatalf("re-entry first = %v paused = %v", a2.First(), a2.Paused())
	}
	a2.Finish(true)
	select {
	case b := <-got:
		if b.Paused() {
			t.Fatal("pause not cleared")
		}
		b.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("b never admitted after the pause cleared")
	}
}

// Release leaves the pause as it was.
func TestGatesReleaseKeepsPause(t *testing.T) {
	g := &EpicGates{}
	ctx := context.Background()
	a, _ := g.Enter(ctx, "ep-1", "a")
	a.Finish(false)
	a2, _ := g.Enter(ctx, "ep-1", "a")
	a2.Release()
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := g.Enter(short, "ep-1", "b"); err == nil {
		t.Fatal("b admitted although the epic is still paused")
	}
	if g.queued("ep-1") != 0 {
		t.Fatal("cancelled waiter stayed in the queue")
	}
}
