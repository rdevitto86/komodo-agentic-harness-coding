package bus

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestSealRefusesPublishAndDrains checks a sealed bus refuses every publish while queued events still reach subscribers.
func TestSealRefusesPublishAndDrains(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "s", TypeRunStop)
	stop(t, b, "before-1")
	stop(t, b, "before-2")
	b.Seal()
	err := b.Publish(Event{Type: TypeRunStop, Data: &RunStop{Target: "after"}})
	if !errors.Is(err, ErrBusSealed) {
		t.Fatalf("Publish after Seal: %v, want ErrBusSealed", err)
	}
	for _, want := range []string{"before-1", "before-2"} {
		if got := recv(t, s).Data.(*RunStop).Target; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	select {
	case e := <-s.Events:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSealRefusesEveryCaller checks publishers on many goroutines all see ErrBusSealed.
func TestSealRefusesEveryCaller(t *testing.T) {
	b := newBus(t, 10)
	b.Seal()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.Publish(Event{Type: TypeRunStop, Data: &RunStop{Target: "x"}}); !errors.Is(err, ErrBusSealed) {
				t.Errorf("got %v, want ErrBusSealed", err)
			}
		}()
	}
	wg.Wait()
}

// TestSealTwiceAndClose checks sealing twice is harmless and a closed bus still reports ErrBusClosed.
func TestSealTwiceAndClose(t *testing.T) {
	b := New(Options{})
	b.Seal()
	b.Seal()
	_ = b.Close()
	err := b.Publish(Event{Type: TypeRunStop, Data: &RunStop{Target: "x"}})
	if !errors.Is(err, ErrBusClosed) {
		t.Fatalf("got %v, want ErrBusClosed", err)
	}
}

// TestSealedPublishQueuesNothing checks a refused publish on a sealed bus queues nothing and drops nothing.
func TestSealedPublishQueuesNothing(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "s")
	b.Seal()
	_ = b.Publish(Event{Type: TypeRunStop, Data: &RunStop{Target: "x"}})
	if s.Dropped() != 0 || len(s.Events) != 0 {
		t.Fatalf("queue %d dropped %d", len(s.Events), s.Dropped())
	}
}
