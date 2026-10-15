// Package fault is the Error Handler: it catches errors and panics, publishes them as error events and prints them to the caller.
package fault

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// Severity says whether work continues after a fault.
type Severity string

const (
	SeverityWarn  Severity = "warn"  // reported; work continues
	SeverityError Severity = "error" // reported; the affected work stopped
)

// Kind picks the CLI's exit code for a fault: 1, 2, 3 or 4.
type Kind string

const (
	KindRefused  Kind = "refused"  // exit 1: Komodo declined on purpose
	KindUsage    Kind = "usage"    // exit 2: the command line was wrong
	KindBlocked  Kind = "blocked"  // exit 3: a state the caller must handle
	KindInternal Kind = "internal" // exit 4: Komodo itself failed
)

// ExitCode returns the process exit code the kind selects; an unknown kind is an internal failure.
func (k Kind) ExitCode() int {
	switch k {
	case KindRefused:
		return 1
	case KindUsage:
		return 2
	case KindBlocked:
		return 3
	default:
		return 4
	}
}

// Source says where a fault came from.
type Source struct {
	Component string // the package, such as "task" or "metrics"
	Run       bus.ID
	Group     bus.ID // empty for a run-level fault
}

// Fault is one thing that went wrong.
type Fault struct {
	Source   Source
	Code     string // a word from the closed code list of the Output Formatter; "internal" for a panic
	Kind     Kind
	Severity Severity
	Reason   string // what went wrong, in plain words
	Fix      string // the command or change that resolves it; may be empty
	Panic    bool
	Stack    string // set for a panic
}

// ExitCode returns the exit code of the command the fault ends; a warn fault never changes it.
func (f Fault) ExitCode() int {
	if f.Severity == SeverityWarn {
		return 0
	}
	return f.Kind.ExitCode()
}

// Error returns the reason, so a Fault can travel as an error.
func (f Fault) Error() string { return f.Reason }

// stackCap is the most bytes of a stack an event keeps, the cap of a block's detail.
const stackCap = 4096

// The subtypes of the fault events.
const (
	subtypeError    bus.Subtype = "fault.error"
	subtypePanic    bus.Subtype = "fault.panic"
	subtypeResource bus.Subtype = "fault.resource"
)

// FaultData holds the fields every fault event carries; each fault subtype embeds it.
type FaultData struct {
	Component string `json:"component"`       // the package the fault came from
	Code      string `json:"code"`            // a word from the closed code list
	Kind      string `json:"kind"`            // refused, usage, blocked or internal
	Severity  string `json:"severity"`        // warn or error
	Reason    string `json:"reason"`          // what went wrong
	Fix       string `json:"fix,omitempty"`   // what resolves it
	Panic     bool   `json:"panic,omitempty"` // true for a recovered panic
	Stack     string `json:"stack,omitempty"` // panic only, cut to 4 KB like a block's detail
	Final     bool   `json:"final,omitempty"` // true for the last event before the program exits
}

// ErrorFault is the data of error / fault.error, a returned error.
type ErrorFault struct{ FaultData }

// EventKey binds ErrorFault to its type and subtype.
func (ErrorFault) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: subtypeError} }

// PanicFault is the data of error / fault.panic, a recovered panic.
type PanicFault struct{ FaultData }

// EventKey binds PanicFault to its type and subtype.
func (PanicFault) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: subtypePanic} }

// ResourceFault is the data of error / fault.resource, from the Resource Manager.
type ResourceFault struct{ FaultData }

// EventKey binds ResourceFault to its type and subtype.
func (ResourceFault) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeError, Subtype: subtypeResource}
}

// RegisterEvents binds the three fault subtypes to their data structs; it panics if called twice.
func RegisterEvents() {
	bus.Register(bus.Key{Type: bus.TypeError, Subtype: subtypeError}, func() bus.Payload { return &ErrorFault{} })
	bus.Register(bus.Key{Type: bus.TypeError, Subtype: subtypePanic}, func() bus.Payload { return &PanicFault{} })
	bus.Register(bus.Key{Type: bus.TypeError, Subtype: subtypeResource}, func() bus.Payload { return &ResourceFault{} })
}

// toData copies a fault into event data, cutting the stack to its cap on a character boundary.
func toData(f Fault) FaultData {
	return FaultData{
		Component: f.Source.Component, Code: f.Code, Kind: string(f.Kind), Severity: string(f.Severity),
		Reason: f.Reason, Fix: f.Fix, Panic: f.Panic, Stack: capStack(f.Stack),
	}
}

// capStack returns s cut to stackCap bytes without splitting a multi-byte character.
func capStack(s string) string {
	if len(s) <= stackCap {
		return s
	}
	n := stackCap
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// fromData builds a fault from event data and the run and group of its event.
func fromData(d FaultData, run, group bus.ID) Fault {
	return Fault{
		Source: Source{Component: d.Component, Run: run, Group: group},
		Code:   d.Code, Kind: Kind(d.Kind), Severity: Severity(d.Severity),
		Reason: d.Reason, Fix: d.Fix, Panic: d.Panic, Stack: d.Stack,
	}
}

// panicReason turns a recovered value into plain words, dropping the runtime's own prefix.
func panicReason(r any) string {
	var s string
	if err, ok := r.(error); ok {
		s = err.Error()
	} else {
		s = fmt.Sprint(r)
	}
	return strings.TrimPrefix(s, "runtime error: ")
}

// otherRow is the fault built from an error event that another package owns.
type otherRow struct {
	component string
	code      format.Code
	kind      Kind
	severity  Severity
	reason    string
}

// otherErrors maps each error subtype that is not a fault event to the fault the Run loop prints for it.
var otherErrors = map[bus.Subtype]otherRow{
	"plan-blocked":    {"intake", format.PlanBlocked, KindBlocked, SeverityError, "The third planner draft failed lint"},
	"plan-timeout":    {"intake", format.PlanTimeout, KindRefused, SeverityError, "The planner's wall clock ran out"},
	"plan-input":      {"intake", format.PlanInput, KindUsage, SeverityError, "A named document or the lock check could not be read"},
	"plan-write":      {"intake", format.PlanWrite, KindInternal, SeverityError, "The new task-list folder could not be written"},
	"context-refused": {"contextmgr", format.ContextRefused, KindRefused, SeverityError, "A skill or tool-server request failed validation"},
	"run-refused":     {"engine", format.RunRefused, KindRefused, SeverityError, "A command was refused, such as a dependency outside the run that is not Done"},
}

// faultFromEvent builds the fault to print for an error event; ok is false for a subtype it does not know.
func faultFromEvent(e bus.Event) (f Fault, ok bool) {
	switch d := e.Data.(type) {
	case *ErrorFault:
		return fromData(d.FaultData, e.Run, e.Group), true
	case *PanicFault:
		return fromData(d.FaultData, e.Run, e.Group), true
	case *ResourceFault:
		return fromData(d.FaultData, e.Run, e.Group), true
	case *bus.BusOverflow:
		return Fault{
			Source: Source{Component: "bus", Run: e.Run, Group: e.Group},
			Code:   string(format.BusOverflow), Kind: KindInternal, Severity: SeverityWarn,
			Reason: fmt.Sprintf("subscription %q dropped %d events", d.Subscription, d.Dropped),
		}, true
	}
	row, ok := otherErrors[e.Subtype]
	if !ok {
		return Fault{}, false
	}
	return Fault{
		Source: Source{Component: row.component, Run: e.Run, Group: e.Group},
		Code:   string(row.code), Kind: row.kind, Severity: row.severity, Reason: row.reason,
	}, true
}

// finalOf reports whether an error event is the last one its process publishes.
func finalOf(e bus.Event) bool {
	switch d := e.Data.(type) {
	case *ErrorFault:
		return d.Final
	case *PanicFault:
		return d.Final
	case *ResourceFault:
		return d.Final
	}
	return false
}
