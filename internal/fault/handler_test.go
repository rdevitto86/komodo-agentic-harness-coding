package fault

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// failBus is a bus whose Publish always fails.
type failBus struct{ bus.Bus }

// Publish refuses every event.
func (failBus) Publish(bus.Event) error { return errors.New("bus down") }

var srcTask = Source{Component: "task", Run: "run-1", Group: "grp-1"}

// TestGoPanic is proof case 1: a panic is published once, onPanic runs once and other goroutines run on.
func TestGoPanic(t *testing.T) {
	r := newRig(t, true)
	var called atomic.Int32
	var got Fault
	done := make(chan struct{})
	r.h.Go(srcTask, func() { panic("boom") }, func(f Fault) { got = f; called.Add(1); close(done) })
	other := make(chan struct{})
	r.h.Go(Source{Component: "task"}, func() { close(other) }, nil)
	<-done
	<-other
	e := r.next(t)
	if e.Type != bus.TypeError || e.Subtype != "fault.panic" {
		t.Fatalf("event %s/%s, want error/fault.panic", e.Type, e.Subtype)
	}
	d := e.Data.(*PanicFault)
	if d.Component != "task" || d.Code != "internal" || d.Kind != "internal" || d.Severity != "error" || !d.Panic || d.Reason != "boom" || d.Final {
		t.Fatalf("data %+v", d.FaultData)
	}
	if e.Run != "run-1" || e.Group != "grp-1" {
		t.Fatalf("run %q group %q", e.Run, e.Group)
	}
	mustContain(t, d.Stack, "goroutine")
	if called.Load() != 1 || got.Reason != "boom" || !got.Panic || got.Kind != KindInternal {
		t.Fatalf("onPanic called %d times with %+v", called.Load(), got)
	}
	r.none(t)
}

// TestGroupPanicBlocksCrash is proof case 2: the starter blocks the panicking group and a second group continues.
func TestGroupPanicBlocksCrash(t *testing.T) {
	r := newRig(t, true)
	var mu sync.Mutex
	blocked := map[bus.ID]string{}
	block := func(f Fault) { mu.Lock(); blocked[f.Source.Group] = "crash"; mu.Unlock() }
	var wg sync.WaitGroup
	wg.Add(2)
	ran := false
	r.h.Go(Source{Component: "task", Group: "g1"}, func() { defer wg.Done(); panic("g1 crashed") }, block)
	r.h.Go(Source{Component: "task", Group: "g2"}, func() { defer wg.Done(); ran = true }, block)
	wg.Wait()
	r.p.waitCalls(t, 1)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if blocked["g1"] != "crash" || len(blocked) != 1 || !ran {
		t.Fatalf("blocked %v, second group ran %v", blocked, ran)
	}
}

// TestRecoverDeferred checks a goroutine that Go did not start is covered by a deferred Recover.
func TestRecoverDeferred(t *testing.T) {
	r := newRig(t, false)
	var onPanic atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer r.h.Recover(srcTask, func(Fault) { onPanic.Add(1) })
		s, i := []int{}, 3
		_ = s[i]
	}()
	<-done
	e := r.next(t)
	d := e.Data.(*PanicFault)
	if !strings.Contains(d.Reason, "index out of range") || onPanic.Load() != 1 {
		t.Fatalf("reason %q, onPanic %d", d.Reason, onPanic.Load())
	}
}

// TestRecoverNoPanic checks Recover does nothing when no panic is in flight.
func TestRecoverNoPanic(t *testing.T) {
	r := newRig(t, false)
	func() { defer r.h.Recover(srcTask, func(Fault) { t.Error("onPanic called") }) }()
	r.none(t)
}

// TestGoPanicsConcurrently runs many panicking goroutines under the race detector.
func TestGoPanicsConcurrently(t *testing.T) {
	r := newRig(t, true)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		r.h.Go(srcTask, func() { panic("x") }, func(Fault) { wg.Done() })
	}
	wg.Wait()
	r.p.waitCalls(t, 40)
}

// TestReportRefused is proof case 3: a refused error is one fault.error event and one print.
func TestReportRefused(t *testing.T) {
	r := newRig(t, true)
	r.h.Report(Source{Component: "config", Run: "run-1"}, "over_limit", KindRefused, errors.New("limits.returns is above 20"))
	e := r.next(t)
	if e.Subtype != "fault.error" {
		t.Fatalf("subtype %s", e.Subtype)
	}
	d := e.Data.(*ErrorFault)
	if d.Kind != "refused" || d.Severity != "error" || d.Code != "over_limit" || d.Component != "config" || d.Reason != "limits.returns is above 20" || d.Panic {
		t.Fatalf("data %+v", d.FaultData)
	}
	got := r.p.waitCalls(t, 1)
	if len(got) != 1 || got[0].Kind != KindRefused || got[0].Source.Run != "run-1" || got[0].Source.Component != "config" {
		t.Fatalf("printed %+v", got)
	}
	r.none(t)
	if len(r.p.calls()) != 1 {
		t.Fatalf("%d prints, want 1", len(r.p.calls()))
	}
}

// TestReportNilError checks a nil error publishes nothing.
func TestReportNilError(t *testing.T) {
	r := newRig(t, true)
	r.h.Report(srcTask, "internal", KindInternal, nil)
	r.none(t)
}

// TestThrowResourceWarn is proof case 4: a warn is one fault.resource event and one print.
func TestThrowResourceWarn(t *testing.T) {
	r := newRig(t, true)
	err := r.h.ThrowResourceError(context.Background(), ResourceError{Kind: "window", Severity: SeverityWarn, Link: "claude-cli", Run: "run-1", Value: 82, Threshold: 80})
	if err != nil {
		t.Fatal(err)
	}
	e := r.next(t)
	if e.Subtype != "fault.resource" || e.Run != "run-1" || e.Group != "" {
		t.Fatalf("event %s run %q group %q", e.Subtype, e.Run, e.Group)
	}
	d := e.Data.(*ResourceFault)
	if d.Severity != "warn" || d.Component != "resource" || d.Code != "over_limit" || d.Kind != "refused" || d.Reason != "plan window 82% used on claude-cli" || d.Fix != "wait for the window reset" {
		t.Fatalf("data %+v", d.FaultData)
	}
	got := r.p.waitCalls(t, 1)
	if got[0].Severity != SeverityWarn || got[0].Source.Component != "resource" {
		t.Fatalf("printed %+v", got[0])
	}
}

// TestThrowResourceOtherKind checks a non-window kind gets a reason built from its fields.
func TestThrowResourceOtherKind(t *testing.T) {
	r := newRig(t, false)
	if err := r.h.ThrowResourceError(context.Background(), ResourceError{Kind: "spend_run", Severity: SeverityError, Value: 5, Threshold: 4}); err != nil {
		t.Fatal(err)
	}
	d := r.next(t).Data.(*ResourceFault)
	if d.Severity != "error" || !strings.Contains(d.Reason, "spend_run") || d.Fix != "" {
		t.Fatalf("data %+v", d.FaultData)
	}
}

// TestThrowResourceFailures checks a dead bus and an ended context are returned as errors.
func TestThrowResourceFailures(t *testing.T) {
	r := newRig(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.h.ThrowResourceError(ctx, ResourceError{Kind: "window"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended context: %v", err)
	}
	_ = r.b.Close()
	err := r.h.ThrowResourceError(context.Background(), ResourceError{Kind: "window", Severity: SeverityWarn})
	if !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("closed bus: %v", err)
	}
	mustContain(t, r.err.String(), "over_limit")
	if err := New(nil, nil).ThrowResourceError(context.Background(), ResourceError{}); err == nil {
		t.Fatal("nil bus returned no error")
	}
}

// TestPrintErrorFails is proof case 5: a failing printer leaves a line on standard error and no new error event.
func TestPrintErrorFails(t *testing.T) {
	r := newRig(t, true)
	r.p.err = errors.New("pipe closed")
	r.h.Report(srcTask, "run_refused", KindRefused, errors.New("dependency not done"))
	r.next(t)
	r.p.waitCalls(t, 1)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(r.err.String(), "pipe closed") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	mustContain(t, r.err.String(), "pipe closed")
	mustContain(t, r.err.String(), "dependency not done")
	r.none(t)
}

// panicPrinter panics on its first call and records the rest.
type panicPrinter struct {
	fakePrinter
	first sync.Once
}

// PrintError panics once, then records.
func (p *panicPrinter) PrintError(f Fault) error {
	p.first.Do(func() { panic("printer bug") })
	return p.fakePrinter.PrintError(f)
}

// TestPanicInsideRunLoop checks a panic in the printer goes to standard error and the loop keeps running.
func TestPanicInsideRunLoop(t *testing.T) {
	b := bus.New(bus.Options{})
	t.Cleanup(func() { _ = b.Close() })
	p := &panicPrinter{}
	h := New(b, p)
	errBuf := &syncBuf{}
	h.stderr = errBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.Report(srcTask, "internal", KindInternal, errors.New("first"))
	h.Report(srcTask, "internal", KindInternal, errors.New("second"))
	p.waitCalls(t, 1)
	if got := p.calls(); got[0].Reason != "second" {
		t.Fatalf("printed %+v", got)
	}
	mustContain(t, errBuf.String(), "printer bug")
}

// TestGuardPanicNilBus is proof case 6: a panicking command is printed and exit 4 is selected.
func TestGuardPanicNilBus(t *testing.T) {
	p := &fakePrinter{}
	h := New(nil, p)
	errBuf := &syncBuf{}
	h.stderr = errBuf
	err := h.Guard(Source{Component: "cli"}, func() error { panic("bad command") })
	var f Fault
	if !errors.As(err, &f) {
		t.Fatalf("Guard returned %v, want a Fault", err)
	}
	if f.Kind != KindInternal || f.Kind.ExitCode() != 4 || !f.Panic || f.Reason != "bad command" {
		t.Fatalf("fault %+v", f)
	}
	got := p.calls()
	if len(got) != 1 || got[0].Reason != "bad command" || got[0].Code != "internal" {
		t.Fatalf("printed %+v", got)
	}
	if errBuf.String() != "" {
		t.Fatalf("standard error: %q", errBuf.String())
	}
}

// TestGuardReturnsError checks a plain error passes through without a print.
func TestGuardReturnsError(t *testing.T) {
	p := &fakePrinter{}
	h := New(nil, p)
	want := errors.New("plain")
	if err := h.Guard(Source{Component: "cli"}, func() error { return want }); err != want {
		t.Fatalf("got %v", err)
	}
	if err := h.Guard(Source{Component: "cli"}, func() error { return nil }); err != nil {
		t.Fatalf("got %v", err)
	}
	if len(p.calls()) != 0 {
		t.Fatal("printed")
	}
}

// TestGuardPrintFails checks a failing printer in the CLI process falls back to standard error.
func TestGuardPrintFails(t *testing.T) {
	p := &fakePrinter{err: errors.New("no tty")}
	h := New(nil, p)
	errBuf := &syncBuf{}
	h.stderr = errBuf
	_ = h.Guard(Source{Component: "cli"}, func() error { panic("x") })
	mustContain(t, errBuf.String(), "no tty")
}

// TestGuardWithBus checks Guard on a bus publishes the panic and the Run loop prints it once.
func TestGuardWithBus(t *testing.T) {
	r := newRig(t, true)
	err := r.h.Guard(srcTask, func() error { panic("in engine") })
	if err == nil {
		t.Fatal("no error")
	}
	if e := r.next(t); e.Subtype != "fault.panic" {
		t.Fatalf("subtype %s", e.Subtype)
	}
	r.p.waitCalls(t, 1)
	r.none(t)
	if len(r.p.calls()) != 1 {
		t.Fatalf("%d prints", len(r.p.calls()))
	}
}

// TestPanicWithClosedBus is proof case 7: a closed bus sends the fault to standard error and onPanic still runs.
func TestPanicWithClosedBus(t *testing.T) {
	r := newRig(t, false)
	_ = r.b.Close()
	done := make(chan Fault, 1)
	r.h.Go(srcTask, func() { panic("late") }, func(f Fault) { done <- f })
	select {
	case f := <-done:
		if f.Reason != "late" {
			t.Fatalf("fault %+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("onPanic not called")
	}
	mustContain(t, r.err.String(), "late")
	if len(r.p.calls()) != 0 {
		t.Fatal("printed through the printer")
	}
}

// TestPublishFailureGoesToStderr checks a bus whose Publish fails sends the fault to standard error.
func TestPublishFailureGoesToStderr(t *testing.T) {
	b := failBus{bus.New(bus.Options{})}
	h := New(b, &fakePrinter{})
	errBuf := &syncBuf{}
	h.stderr = errBuf
	h.Report(srcTask, "internal", KindInternal, errors.New("lost"))
	mustContain(t, errBuf.String(), "lost")
}

// TestNilBusReport checks Report with no bus writes to standard error.
func TestNilBusReport(t *testing.T) {
	h := New(nil, &fakePrinter{})
	errBuf := &syncBuf{}
	h.stderr = errBuf
	h.Report(srcTask, "internal", KindInternal, errors.New("nowhere"))
	mustContain(t, errBuf.String(), "nowhere")
}

// TestFinalRefused is proof case 9: one final event, one print before Final returns, exit 1.
func TestFinalRefused(t *testing.T) {
	r := newRig(t, true)
	f := Fault{Source: Source{Component: "config", Run: "run-1"}, Code: "over_limit", Kind: KindRefused, Severity: SeverityError, Reason: "too big"}
	code := r.h.Final(context.Background(), f.Source, f)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if n := len(r.p.calls()); n != 1 {
		t.Fatalf("%d prints when Final returned, want 1", n)
	}
	e := r.next(t)
	d := e.Data.(*ErrorFault)
	if e.Subtype != "fault.error" || !d.Final || d.Reason != "too big" {
		t.Fatalf("event %s %+v", e.Subtype, d.FaultData)
	}
	r.none(t)
}

// TestFinalPanicSubtype checks a panic fault ends as fault.panic with final set and exit 4.
func TestFinalPanicSubtype(t *testing.T) {
	r := newRig(t, true)
	f := Fault{Code: "internal", Kind: KindInternal, Severity: SeverityError, Reason: "x", Panic: true, Stack: "s"}
	if code := r.h.Final(context.Background(), srcTask, f); code != 4 {
		t.Fatalf("exit %d", code)
	}
	e := r.next(t)
	if e.Subtype != "fault.panic" || !e.Data.(*PanicFault).Final {
		t.Fatalf("event %s", e.Subtype)
	}
}

// TestFinalClosedBus is proof case 10: a closed bus sends the fault to standard error and the exit code stands.
func TestFinalClosedBus(t *testing.T) {
	r := newRig(t, false)
	_ = r.b.Close()
	code := r.h.Final(context.Background(), srcTask, Fault{Code: "plan_blocked", Kind: KindBlocked, Severity: SeverityError, Reason: "lint failed"})
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	mustContain(t, r.err.String(), "lint failed")
}

// TestFinalNilBus checks the CLI process prints through the printer and publishes nothing.
func TestFinalNilBus(t *testing.T) {
	p := &fakePrinter{}
	h := New(nil, p)
	errBuf := &syncBuf{}
	h.stderr = errBuf
	code := h.Final(context.Background(), Source{Component: "cli"}, Fault{Code: "usage", Kind: KindUsage, Severity: SeverityError, Reason: "unknown flag"})
	if code != 2 || len(p.calls()) != 1 || errBuf.String() != "" {
		t.Fatalf("exit %d, prints %d, stderr %q", code, len(p.calls()), errBuf.String())
	}
}

// TestFinalContextEnds checks Final returns its exit code when no loop prints before ctx ends.
func TestFinalContextEnds(t *testing.T) {
	r := newRig(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	code := r.h.Final(ctx, srcTask, Fault{Code: "internal", Kind: KindInternal, Severity: SeverityError, Reason: "x"})
	if code != 4 {
		t.Fatalf("exit %d", code)
	}
}

// TestNoEventAfterFinal is proof case 11: after Final nothing else leaves the process.
func TestNoEventAfterFinal(t *testing.T) {
	r := newRig(t, true)
	r.h.Final(context.Background(), srcTask, Fault{Code: "internal", Kind: KindInternal, Severity: SeverityError, Reason: "last"})
	r.next(t)
	r.h.Report(srcTask, "internal", KindInternal, errors.New("after"))
	r.h.Go(srcTask, func() { panic("after panic") }, nil)
	_ = r.h.ThrowResourceError(context.Background(), ResourceError{Kind: "window", Severity: SeverityWarn})
	if code := r.h.Final(context.Background(), srcTask, Fault{Code: "internal", Kind: KindInternal, Severity: SeverityError, Reason: "second"}); code != 4 {
		t.Fatalf("second Final exit %d", code)
	}
	r.none(t)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(r.err.String(), "after panic") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	mustContain(t, r.err.String(), "after")
	mustContain(t, r.err.String(), "after panic")
	mustContain(t, r.err.String(), "second")
	if n := len(r.p.calls()); n != 1 {
		t.Fatalf("%d prints, want 1", n)
	}
}

// TestRunBusOverflow checks a bus.overflow event is printed as the warning of the contract table.
func TestRunBusOverflow(t *testing.T) {
	r := newRig(t, true)
	if err := r.b.Publish(bus.Event{Type: bus.TypeError, Run: "run-1", Data: &bus.BusOverflow{Subscription: "metrics", Dropped: 7}}); err == nil {
		t.Fatal("empty subtype accepted")
	}
	if err := r.b.Publish(bus.Event{Type: bus.TypeError, Subtype: "bus.overflow", Run: "run-1", Data: &bus.BusOverflow{Subscription: "metrics", Dropped: 7}}); err != nil {
		t.Fatal(err)
	}
	f := r.p.waitCalls(t, 1)[0]
	if f.Code != "bus_overflow" || f.Kind != KindInternal || f.Severity != SeverityWarn || f.Source.Component != "bus" || f.Fix != "" || f.Source.Run != "run-1" {
		t.Fatalf("fault %+v", f)
	}
	mustContain(t, f.Reason, "metrics")
	mustContain(t, f.Reason, "7")
}

// TestRunOtherErrorSubtypes checks each non-fault error subtype is printed with its table row.
func TestRunOtherErrorSubtypes(t *testing.T) {
	r := newRig(t, true)
	for st, m := range otherErrors {
		if err := r.b.Publish(bus.Event{Type: bus.TypeError, Subtype: st, Run: "run-1", Group: "g1", Data: &otherBody{st: st}}); err != nil {
			t.Fatal(err)
		}
		_ = m
	}
	got := r.p.waitCalls(t, len(otherErrors))
	seen := map[string]Fault{}
	for _, f := range got {
		seen[f.Code] = f
	}
	for _, m := range otherErrors {
		f, ok := seen[string(m.code)]
		if !ok {
			t.Errorf("%s not printed", m.code)
			continue
		}
		if f.Kind != m.kind || f.Severity != m.severity || f.Source.Component != m.component || f.Fix != "" || f.Source.Group != "g1" || f.Reason == "" {
			t.Errorf("%s: %+v", m.code, f)
		}
	}
}

// TestRunFlattensFaultData checks a fault event becomes a Fault field for field.
func TestRunFlattensFaultData(t *testing.T) {
	r := newRig(t, true)
	d := FaultData{Component: "task", Code: "internal", Kind: "internal", Severity: "error", Reason: "r", Fix: "f", Panic: true, Stack: "s"}
	if err := r.b.Publish(bus.Event{Type: bus.TypeError, Subtype: "fault.panic", Run: "run-1", Group: "g1", Data: &PanicFault{d}}); err != nil {
		t.Fatal(err)
	}
	want := Fault{Source: Source{Component: "task", Run: "run-1", Group: "g1"}, Code: "internal", Kind: KindInternal, Severity: SeverityError, Reason: "r", Fix: "f", Panic: true, Stack: "s"}
	if got := r.p.waitCalls(t, 1)[0]; got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// TestRunUnknownErrorSubtype checks an error subtype with no row is written to standard error, not printed.
func TestRunUnknownErrorSubtype(t *testing.T) {
	r := newRig(t, true)
	if err := r.b.Publish(bus.Event{Type: bus.TypeError, Subtype: "mystery", Data: &otherBody{st: "mystery"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(r.err.String(), "mystery") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	mustContain(t, r.err.String(), "mystery")
	if len(r.p.calls()) != 0 {
		t.Fatal("printed")
	}
}

// TestRunEnds checks Run returns when ctx ends and when the bus has no subscription for it.
func TestRunEnds(t *testing.T) {
	r := newRig(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.h.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
	if err := New(nil, &fakePrinter{}).Run(context.Background()); err == nil {
		t.Fatal("Run with no bus returned nil")
	}
	closed := bus.New(bus.Options{})
	_ = closed.Close()
	if err := New(closed, &fakePrinter{}).Run(context.Background()); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("closed bus: %v", err)
	}
}
