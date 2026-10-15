package fault

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// TestMain binds the fault events once, as the program does before its first publish.
func TestMain(m *testing.M) {
	RegisterEvents()
	// The owning chapters of these error subtypes are not built; a stand-in body lets them be published.
	for _, st := range []bus.Subtype{"plan-blocked", "plan-timeout", "plan-input", "plan-write", "context-refused", "run-refused", "mystery"} {
		bus.Register(bus.Key{Type: bus.TypeError, Subtype: st}, func() bus.Payload { return &otherBody{st: st} })
	}
	os.Exit(m.Run())
}

// otherBody is a stand-in data body for an error subtype that another package owns.
type otherBody struct{ st bus.Subtype }

// EventKey binds the stand-in to its subtype.
func (o otherBody) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: o.st} }

// fakePrinter records every fault it is asked to print.
type fakePrinter struct {
	mu  sync.Mutex
	got []Fault
	err error
}

// PrintError records f and returns the configured error.
func (p *fakePrinter) PrintError(f Fault) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, f)
	return p.err
}

// calls returns a copy of the faults printed so far.
func (p *fakePrinter) calls() []Fault {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Fault(nil), p.got...)
}

// waitCalls waits until n faults were printed and returns them.
func (p *fakePrinter) waitCalls(t *testing.T, n int) []Fault {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := p.calls(); len(got) >= n {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("want %d PrintError calls, got %d", n, len(p.calls()))
	return nil
}

// syncBuf is a goroutine-safe buffer that stands in for standard error.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends p to the buffer.
func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything written so far.
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is a handler on a real bus with a fake printer, an observer and a captured standard error.
type rig struct {
	h   *Handler
	b   bus.Bus
	p   *fakePrinter
	obs *bus.Subscription
	err *syncBuf
}

// newRig builds a rig; the Run loop is started only when run is true.
func newRig(t *testing.T, run bool) *rig {
	t.Helper()
	b := bus.New(bus.Options{})
	p := &fakePrinter{}
	h := New(b, p)
	r := &rig{h: h, b: b, p: p, err: &syncBuf{}}
	h.stderr = r.err
	obs, err := b.Subscribe("observer", bus.TypeError)
	if err != nil {
		t.Fatal(err)
	}
	r.obs = obs
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	if run {
		go func() { defer close(done); _ = h.Run(ctx) }()
	} else {
		close(done)
	}
	t.Cleanup(func() {
		cancel()
		<-done
		_ = b.Close()
	})
	if run {
		waitRunning(t, h)
	}
	return r
}

// waitRunning waits until the Run loop of h is reading.
func waitRunning(t *testing.T, h *Handler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !h.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Run loop did not start")
		}
		time.Sleep(time.Millisecond)
	}
}

// gatePrinter blocks on a fault whose reason is "block" until its gate opens.
type gatePrinter struct {
	fakePrinter
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

// open lets the held fault through; calling it twice is harmless.
func (p *gatePrinter) open() { p.once.Do(func() { close(p.gate) }) }

// PrintError holds a "block" fault until the gate opens, then records it.
func (p *gatePrinter) PrintError(f Fault) error {
	if f.Reason == "block" {
		close(p.entered)
		<-p.gate
	}
	return p.fakePrinter.PrintError(f)
}

// newFullRig builds a handler whose one-slot queue is full: its loop is held in the printer and a second fault waits behind it.
func newFullRig(t *testing.T) (*Handler, *gatePrinter, *syncBuf) {
	t.Helper()
	b := bus.New(bus.Options{BufferEvents: 1})
	p := &gatePrinter{entered: make(chan struct{}), gate: make(chan struct{})}
	h := New(b, p)
	h.finalWait = 50 * time.Millisecond
	errBuf := &syncBuf{}
	h.stderr = errBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx) }()
	t.Cleanup(func() {
		p.open()
		cancel()
		<-done
		_ = b.Close()
	})
	waitRunning(t, h)
	h.Report(srcTask, "internal", KindInternal, errors.New("block"))
	<-p.entered
	h.Report(srcTask, "internal", KindInternal, errors.New("fill"))
	return h, p, errBuf
}

// next reads the next event the observer sees or fails after a second.
func (r *rig) next(t *testing.T) bus.Event {
	t.Helper()
	select {
	case e, ok := <-r.obs.Events:
		if !ok {
			t.Fatal("observer closed")
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("no event within 1s")
	}
	return bus.Event{}
}

// none fails when the observer sees any event within a short wait.
func (r *rig) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-r.obs.Events:
		t.Fatalf("unexpected event %s/%s", e.Type, e.Subtype)
	case <-time.After(80 * time.Millisecond):
	}
}

// mustContain fails when s lacks sub.
func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%q does not contain %q", s, sub)
	}
}
