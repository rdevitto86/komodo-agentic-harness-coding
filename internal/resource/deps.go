package resource

import (
	"context"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

// Subscriber is the Event Bus as the Resource Manager uses it.
type Subscriber interface {
	// Subscribe returns a private queue of events of the given types.
	Subscribe(name string, types ...bus.Type) (*bus.Subscription, error)
}

// Thrower is the Error Handler as the Resource Manager uses it.
type Thrower interface {
	// ThrowResourceError records a resource warning or error and sends it to the caller through
	// the CLI.
	ThrowResourceError(ctx context.Context, e fault.ResourceError) error
	// Report publishes an ordinary error that the Resource Manager cannot act on itself.
	Report(src fault.Source, code string, kind fault.Kind, err error)
}

// Pauser is the Runtime Manager as the Resource Manager uses it.
type Pauser interface {
	// PauseGroups pauses each named group after its current step; the Runtime Manager resumes them at Until.
	PauseGroups(ctx context.Context, o PauseOrder) error
	// NudgeGroups sends the token-cap nudge to the running agents of each named group.
	NudgeGroups(ctx context.Context, o NudgeOrder) error
}

// Records is the State Manager as the Resource Manager uses it.
type Records interface {
	// LoadWindows returns every stored window record.
	LoadWindows(ctx context.Context) (map[WindowKey]WindowRecord, error)
	// SaveWindow stores one window record.
	SaveWindow(ctx context.Context, k WindowKey, r WindowRecord) error
}

// Deps are the parts the Resource Manager calls.
type Deps struct {
	Bus     Subscriber // delivers events of type resource
	Errors  Thrower
	Runtime Pauser
	State   Records
	Now     func() time.Time // the clock; nil means time.Now
}
