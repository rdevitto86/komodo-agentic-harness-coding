package fault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// errNoBus marks a publish on a handler that has no bus, as in the CLI process.
var errNoBus = errors.New("no event bus")

// errAfterFinal marks an event refused because the final event already left the process.
var errAfterFinal = errors.New("final event already published")

// Handler catches faults, publishes them and prints them.
type Handler struct {
	bus     bus.Bus
	printer Printer
	sub     *bus.Subscription // the Run loop's queue of error events; nil without a bus
	subErr  error             // why the subscription could not be made
	stderr  io.Writer

	mu        sync.Mutex // orders every publish against the final one
	finalSent bool       // the final event was published; nothing follows it

	printed     chan struct{} // closed once the Run loop has printed the final event
	printedOnce sync.Once

	errMu sync.Mutex // keeps lines on standard error whole
}

// New returns a Handler that publishes on b and prints through p; b is nil in the CLI process, where Guard and Final print through p and publish nothing.
func New(b bus.Bus, p Printer) *Handler {
	h := &Handler{bus: b, printer: p, stderr: os.Stderr, printed: make(chan struct{})}
	if b != nil {
		h.sub, h.subErr = b.Subscribe("fault", bus.TypeError)
	}
	return h
}

// Go runs fn in a new goroutine; a panic is recovered, published, and then passed to onPanic.
func (h *Handler) Go(src Source, fn func(), onPanic func(Fault)) {
	go func() {
		defer h.Recover(src, onPanic)
		fn()
	}()
}

// Recover is deferred at the top of a goroutine that Go did not start.
func (h *Handler) Recover(src Source, onPanic func(Fault)) {
	r := recover()
	if r == nil {
		return
	}
	f := panicFault(src, r)
	h.emit(f, subtypePanic)
	if onPanic != nil {
		onPanic(f)
	}
}

// Guard runs fn in the calling goroutine and returns its error, or a Fault for its panic.
// Without a bus it prints the panic through the printer; with one it publishes it and the Run loop prints.
func (h *Handler) Guard(src Source, fn func() error) (err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		f := panicFault(src, r)
		if h.bus == nil {
			h.print(f)
		} else {
			h.emit(f, subtypePanic)
		}
		err = f
	}()
	return fn()
}

// Report publishes a returned error as an error event.
func (h *Handler) Report(src Source, code string, kind Kind, err error) {
	defer h.guardSelf()
	if err == nil {
		return
	}
	h.emit(Fault{Source: src, Code: code, Kind: kind, Severity: SeverityError, Reason: err.Error()}, subtypeError)
}

// TODO(G-225): the entry point for what the Metrics Service sends the Error Handler goes here.

// Final publishes the one last error event with Final set, waits until the Run loop has printed it or ctx ends, and returns the exit code of the fault's Kind.
// Without a bus it prints through the printer and publishes nothing.
func (h *Handler) Final(ctx context.Context, src Source, f Fault) int {
	code := f.Kind.ExitCode()
	if src != (Source{}) {
		f.Source = src
	}
	if h.bus == nil {
		h.print(f)
		return code
	}
	if err := h.publish(f, subtypeOf(f), true); err != nil {
		h.writeStderr(f, "not published: "+err.Error())
		return code
	}
	if h.sub == nil {
		h.print(f)
		return code
	}
	select {
	case <-h.printed:
	case <-ctx.Done():
	}
	return code
}

// Run reads error events and prints each through the Printer until ctx ends.
func (h *Handler) Run(ctx context.Context) error {
	if h.sub == nil {
		if h.subErr != nil {
			return h.subErr
		}
		return errNoBus
	}
	defer h.sub.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-h.sub.Events:
			if !ok {
				return bus.ErrBusClosed
			}
			h.handle(e)
		}
	}
}

// handle prints the fault in one error event; a panic here goes to standard error and the loop goes on.
func (h *Handler) handle(e bus.Event) {
	if finalOf(e) {
		defer h.printedOnce.Do(func() { close(h.printed) })
	}
	defer h.guardSelf()
	f, ok := faultFromEvent(e)
	if !ok {
		h.writeLine(fmt.Sprintf("komodo: unhandled error event %s\n", e.Subtype))
		return
	}
	h.print(f)
}

// guardSelf is deferred in the handler's own paths; it writes a panic to standard error and returns.
func (h *Handler) guardSelf() {
	if r := recover(); r != nil {
		h.writeLine(fmt.Sprintf("komodo: error handler recovered: %s\n%s", panicReason(r), debug.Stack()))
	}
}

// print hands f to the printer; a failure goes to standard error and raises no event.
func (h *Handler) print(f Fault) {
	if h.printer == nil {
		h.writeStderr(f, "no printer")
		return
	}
	if err := h.printer.PrintError(f); err != nil {
		h.writeStderr(f, "print failed: "+err.Error())
	}
}

// emit publishes f under the subtype and writes it to standard error when it cannot be published.
func (h *Handler) emit(f Fault, st bus.Subtype) {
	if err := h.publish(f, st, false); err != nil {
		h.writeStderr(f, "not published: "+err.Error())
	}
}

// publish puts f on the bus as an error event; once the final event is out, nothing else is published.
func (h *Handler) publish(f Fault, st bus.Subtype, final bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bus == nil {
		return errNoBus
	}
	if h.finalSent {
		return errAfterFinal
	}
	d := toData(f)
	d.Final = final
	var p bus.Payload
	switch st {
	case subtypePanic:
		p = &PanicFault{d}
	case subtypeResource:
		p = &ResourceFault{d}
	default:
		p = &ErrorFault{d}
	}
	if final {
		h.finalSent = true
	}
	return h.bus.Publish(bus.Event{Type: bus.TypeError, Subtype: st, Run: f.Source.Run, Group: f.Source.Group, Data: p})
}

// subtypeOf returns the subtype a fault is published under.
func subtypeOf(f Fault) bus.Subtype {
	if f.Panic {
		return subtypePanic
	}
	return subtypeError
}

// panicFault builds the internal fault for a recovered value, with the stack of the panic.
func panicFault(src Source, r any) Fault {
	return Fault{
		Source: src, Code: string(format.Internal), Kind: KindInternal, Severity: SeverityError,
		Reason: panicReason(r), Panic: true, Stack: string(debug.Stack()),
	}
}

// writeStderr writes f and the note on why it is here to standard error.
func (h *Handler) writeStderr(f Fault, note string) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "komodo: %s %s in %s: %s (%s)\n", f.Severity, f.Code, f.Source.Component, f.Reason, note)
	if f.Fix != "" {
		fmt.Fprintf(&sb, "fix: %s\n", f.Fix)
	}
	if f.Stack != "" {
		sb.WriteString(f.Stack + "\n")
	}
	h.writeLine(sb.String())
}

// writeLine writes s to standard error under the lock that keeps concurrent lines whole.
func (h *Handler) writeLine(s string) {
	h.errMu.Lock()
	defer h.errMu.Unlock()
	_, _ = io.WriteString(h.stderr, s)
}
