package bus

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// panicFault stands in for the Error Handler's panic fault body, which that package owns.
type panicFault struct{ Value string }

// EventKey binds panicFault to a test-only subtype.
func (panicFault) EventKey() Key { return Key{TypeError, "test.panic"} }

var registerPanicFaultOnce sync.Once

// newBus returns a bus with the given queue size, closed when the test ends.
func newBus(t *testing.T, buf int) Bus {
	t.Helper()
	b := New(Options{BufferEvents: buf})
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// sub subscribes and fails the test on error.
func sub(t *testing.T, b Bus, name string, types ...Type) *Subscription {
	t.Helper()
	s, err := b.Subscribe(name, types...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// stop publishes a run-stop event whose target carries a label.
func stop(t *testing.T, b Bus, label string) {
	t.Helper()
	if err := b.Publish(Event{Type: TypeRunStop, Data: &RunStop{Target: label}}); err != nil {
		t.Fatal(err)
	}
}

// recv reads one event or fails after a second.
func recv(t *testing.T, s *Subscription) Event {
	t.Helper()
	select {
	case e, ok := <-s.Events:
		if !ok {
			t.Fatal("channel closed")
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("no event within 1s")
	}
	return Event{}
}

// drain returns every event already queued, without waiting.
func drain(s *Subscription) []Event {
	var out []Event
	for {
		select {
		case e, ok := <-s.Events:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

// labels lists the run-stop targets of events.
func labels(es []Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Data.(*RunStop).Target
	}
	return out
}

// 1,000 events from 4 publishers reach 3 subscribers in one common order.
func TestSameOrderForEverySubscriber(t *testing.T) {
	b := newBus(t, 1000)
	subs := []*Subscription{sub(t, b, "a"), sub(t, b, "b", TypeRunStop), sub(t, b, "c", TypeRunStop, TypeError)}
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				stop(t, b, fmt.Sprintf("p%d-%03d", p, i))
			}
		}()
	}
	wg.Wait()
	first := labels(drain(subs[0]))
	if len(first) != 1000 {
		t.Fatalf("first subscriber saw %d events, want 1000", len(first))
	}
	for i, s := range subs[1:] {
		got := labels(drain(s))
		if fmt.Sprint(got) != fmt.Sprint(first) {
			t.Fatalf("subscriber %d saw a different order", i+1)
		}
	}
	next := map[int]int{}
	for _, l := range first {
		var p, n int
		if _, err := fmt.Sscanf(l, "p%d-%d", &p, &n); err != nil {
			t.Fatal(err)
		}
		if n != next[p] {
			t.Fatalf("publisher %d: got %d, want %d", p, n, next[p])
		}
		next[p]++
	}
}

// A run-status subscriber never receives an error event.
func TestTypeFilter(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "status", TypeRunStatus)
	if err := b.Publish(Event{Type: TypeError, Subtype: "bus.overflow", Data: &BusOverflow{}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(Event{Type: TypeRunStatus, Subtype: "task.done", Data: &TaskDone{Task: "x"}}); err != nil {
		t.Fatal(err)
	}
	got := drain(s)
	if len(got) != 1 || got[0].Type != TypeRunStatus {
		t.Fatalf("got %+v, want only the run-status event", got)
	}
}

// Publish stamps the time in UTC, whole seconds, over whatever the publisher set.
func TestPublishStampsTime(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "s")
	stop(t, b, "x")
	e := recv(t, s)
	if e.Time.Location() != time.UTC || e.Time.Nanosecond() != 0 || time.Since(e.Time) > 5*time.Second {
		t.Fatalf("time %v not a fresh whole-second UTC stamp", e.Time)
	}
}

// A subscriber that never reads, queue 50: events 51 on are dropped, Dropped rises, one bus.overflow is published.
func TestOverflowDropsAndPublishesOnce(t *testing.T) {
	b := newBus(t, 50)
	stuck := sub(t, b, "stuck", TypeRunStop)
	errs := sub(t, b, "errors", TypeError)
	for i := 0; i < 60; i++ {
		stop(t, b, fmt.Sprint(i))
	}
	if got := stuck.Dropped(); got != 10 {
		t.Fatalf("Dropped %d, want 10", got)
	}
	if got := labels(drain(stuck)); len(got) != 50 || got[0] != "0" || got[49] != "49" {
		t.Fatalf("queue holds %d events %v, want 0..49", len(got), got)
	}
	ov := drain(errs)
	if len(ov) != 1 {
		t.Fatalf("%d error events, want 1", len(ov))
	}
	d, ok := ov[0].Data.(*BusOverflow)
	if !ok || ov[0].Subtype != "bus.overflow" || d.Subscription != "stuck" || d.Dropped != 1 {
		t.Fatalf("overflow event wrong: %+v", ov[0])
	}
	if ov[0].Run != "" || ov[0].Group != "" {
		t.Fatalf("overflow event carries a run or group: %+v", ov[0])
	}
}

// A new burst after the queue drains publishes a second bus.overflow.
func TestOverflowOncePerBurst(t *testing.T) {
	b := newBus(t, 2)
	stuck := sub(t, b, "stuck", TypeRunStop)
	errs := sub(t, b, "errors", TypeError)
	for i := 0; i < 5; i++ {
		stop(t, b, fmt.Sprint(i))
	}
	drain(stuck)
	for i := 0; i < 5; i++ {
		stop(t, b, fmt.Sprint(i))
	}
	if n := len(drain(errs)); n != 2 {
		t.Fatalf("%d overflow events over two bursts, want 2", n)
	}
	if got := stuck.Dropped(); got != 6 {
		t.Fatalf("Dropped %d, want 6", got)
	}
}

// A full queue that takes every type cannot feed itself: its own bus.overflow drops, is counted, and publishes nothing more.
func TestOverflowDoesNotRecurse(t *testing.T) {
	b := newBus(t, 3)
	all := sub(t, b, "metrics")
	errs := sub(t, b, "errors", TypeError)
	for i := 0; i < 10; i++ {
		stop(t, b, fmt.Sprint(i))
	}
	if n := len(drain(errs)); n != 1 {
		t.Fatalf("%d overflow events, want 1", n)
	}
	if got := all.Dropped(); got != 8 {
		t.Fatalf("Dropped %d, want 7 stops plus the overflow event", got)
	}
}

// Two subscribers that keep up see a bus.overflow caused by a third at the same place in the stream.
func TestOverflowKeepsCommonOrder(t *testing.T) {
	b := newBus(t, 5)
	sub(t, b, "stuck", TypeRunStop)
	a, c := sub(t, b, "a"), sub(t, b, "c")
	var seenA, seenC []string
	for i := 0; i < 8; i++ {
		stop(t, b, fmt.Sprint(i))
		for _, e := range drain(a) {
			seenA = append(seenA, string(e.Type)+"/"+string(e.Subtype))
		}
		for _, e := range drain(c) {
			seenC = append(seenC, string(e.Type)+"/"+string(e.Subtype))
		}
	}
	if fmt.Sprint(seenA) != fmt.Sprint(seenC) {
		t.Fatalf("orders differ:\n%v\n%v", seenA, seenC)
	}
	if len(seenA) != 9 || seenA[5] != "run-stop/" || seenA[6] != "error/bus.overflow" {
		t.Fatalf("overflow not placed right after the stop that caused it: %v", seenA)
	}
}

// A slow subscriber and a fast one: the fast one receives every event and Publish stays quick.
func TestSlowSubscriberDoesNotDelayFastOne(t *testing.T) {
	b := newBus(t, 20)
	slow := sub(t, b, "slow", TypeRunStop)
	fast := sub(t, b, "fast", TypeRunStop)
	var worst time.Duration
	for i := 0; i < 100; i++ {
		start := time.Now()
		stop(t, b, fmt.Sprint(i))
		if d := time.Since(start); d > worst {
			worst = d
		}
		if got := recv(t, fast).Data.(*RunStop).Target; got != fmt.Sprint(i) {
			t.Fatalf("fast got %s, want %d", got, i)
		}
	}
	if worst > 50*time.Millisecond {
		t.Fatalf("slowest Publish took %v", worst)
	}
	if slow.Dropped() != 80 || fast.Dropped() != 0 {
		t.Fatalf("dropped slow %d fast %d, want 80 and 0", slow.Dropped(), fast.Dropped())
	}
}

// runCatching runs fn in a goroutine that publishes an error event when fn panics and then closes the subscription, as the Error Handler's helper does.
func runCatching(b Bus, s *Subscription, fn func(), done chan<- struct{}) {
	registerPanicFaultOnce.Do(func() {
		Register(Key{TypeError, "test.panic"}, func() Payload { return &panicFault{} })
	})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				_ = b.Publish(Event{Type: TypeError, Subtype: "test.panic", Data: &panicFault{Value: fmt.Sprint(r)}})
				s.Close()
			}
		}()
		fn()
	}()
}

// A subscriber goroutine that panics yields one error event and leaves the other subscribers unaffected.
func TestPanickingSubscriberIsolated(t *testing.T) {
	b := newBus(t, 10)
	bad := sub(t, b, "bad", TypeRunStop)
	good := sub(t, b, "good", TypeRunStop)
	errs := sub(t, b, "errors", TypeError)
	done := make(chan struct{})
	runCatching(b, bad, func() {
		<-bad.Events
		panic("boom")
	}, done)
	stop(t, b, "1")
	<-done
	stop(t, b, "2")
	stop(t, b, "3")
	if got := labels(drain(good)); fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("good subscriber saw %v", got)
	}
	ev := drain(errs)
	if len(ev) != 1 || ev[0].Subtype != "test.panic" {
		t.Fatalf("error events %+v, want one panic fault", ev)
	}
	if _, open := <-bad.Events; open {
		t.Fatal("panicked subscriber still open")
	}
}

// Publish of an unregistered subtype, an unknown type, a wrong struct or no data returns ErrUnknownEvent and queues nothing.
func TestPublishRefusesUnknown(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "s")
	var nilStop *RunStop
	cases := map[string]Event{
		"subtype":     {Type: TypeRunStatus, Subtype: "nope.nope", Data: &TaskDone{}},
		"type":        {Type: "bogus", Data: &RunStop{}},
		"wrong data":  {Type: TypeRunStop, Data: &RunStart{}},
		"nil data":    {Type: TypeRunStop},
		"nil pointer": {Type: TypeRunStop, Data: nilStop},
		"bad subtype": {Type: TypeRunStop, Subtype: "x", Data: &RunStop{}},
	}
	for name, e := range cases {
		if err := b.Publish(e); !errors.Is(err, ErrUnknownEvent) {
			t.Errorf("%s: err %v, want ErrUnknownEvent", name, err)
		}
	}
	if got := drain(s); len(got) != 0 {
		t.Fatalf("queued %d events after refused publishes", len(got))
	}
}

// Publish takes a struct value as well as a pointer.
func TestPublishAcceptsValueData(t *testing.T) {
	b := newBus(t, 10)
	s := sub(t, b, "s")
	if err := b.Publish(Event{Type: TypeRunStop, Data: RunStop{Target: "v"}}); err != nil {
		t.Fatal(err)
	}
	if recv(t, s).Data.(RunStop).Target != "v" {
		t.Fatal("wrong data")
	}
}

// Close then Publish returns ErrBusClosed, every Events channel is closed, and Subscribe also refuses.
func TestClose(t *testing.T) {
	b := New(Options{BufferEvents: 5})
	s1, s2 := sub(t, b, "a"), sub(t, b, "b", TypeError)
	stop(t, b, "before")
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(Event{Type: TypeRunStop, Data: &RunStop{}}); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("Publish err %v, want ErrBusClosed", err)
	}
	if _, err := b.Subscribe("late"); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("Subscribe err %v, want ErrBusClosed", err)
	}
	if got := labels(drain(s1)); fmt.Sprint(got) != "[before]" {
		t.Fatalf("queued event lost on close: %v", got)
	}
	if _, open := <-s1.Events; open {
		t.Fatal("s1 channel open")
	}
	if _, open := <-s2.Events; open {
		t.Fatal("s2 channel open")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	s1.Close()
}

// A closed subscription gets no later events and its channel is closed; others continue.
func TestSubscriptionClose(t *testing.T) {
	b := newBus(t, 5)
	s1, s2 := sub(t, b, "a"), sub(t, b, "b")
	s1.Close()
	s1.Close()
	stop(t, b, "x")
	if _, open := <-s1.Events; open {
		t.Fatal("closed subscription still open")
	}
	if len(drain(s2)) != 1 {
		t.Fatal("other subscriber lost the event")
	}
}

// Publish, Subscribe and subscription Close can race without a data race or deadlock.
func TestConcurrentUse(t *testing.T) {
	b := newBus(t, 4)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s, err := b.Subscribe(fmt.Sprint(i, j))
				if err != nil {
					return
				}
				_ = b.Publish(Event{Type: TypeRunStop, Data: &RunStop{}})
				s.Close()
				_ = s.Dropped()
			}
		}()
	}
	wg.Wait()
}

// A run-start with schedule set is broadcast once, with groups, source and schedule intact.
func TestRunStartBroadcastOnce(t *testing.T) {
	b := newBus(t, 10)
	rt := sub(t, b, "runtime", TypeRunStart)
	at := time.Date(2026, 10, 16, 2, 0, 0, 0, time.FixedZone("", -7*3600))
	err := b.Publish(Event{Type: TypeRunStart, Run: "r1", Data: &RunStart{Target: "tg-1.2", Groups: []string{"tg-1.2"}, Source: "cli", Schedule: at}})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(rt)
	if len(got) != 1 {
		t.Fatalf("%d events, want 1", len(got))
	}
	d := got[0].Data.(*RunStart)
	if d.Source != "cli" || fmt.Sprint(d.Groups) != "[tg-1.2]" || !d.Schedule.Equal(at) {
		t.Fatalf("data wrong: %+v", d)
	}
}

// A zero BufferEvents falls back to the default of 50.
func TestZeroBufferUsesDefault(t *testing.T) {
	b := newBus(t, 0)
	s := sub(t, b, "s", TypeRunStop)
	for i := 0; i < 60; i++ {
		stop(t, b, fmt.Sprint(i))
	}
	if s.Dropped() != 10 {
		t.Fatalf("Dropped %d, want 10", s.Dropped())
	}
}
