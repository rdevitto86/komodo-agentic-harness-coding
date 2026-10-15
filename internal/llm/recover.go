package llm

import (
	"fmt"
	"os"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

// Recoverer is the part of the fault handler that guards a goroutine; every goroutine here defers it first.
type Recoverer interface {
	// Recover turns a panic of the goroutine into a published fault.
	Recover(src fault.Source, onPanic func(fault.Fault))
}

// stderrPrinter shows a fault on standard error when no engine printer is wired.
type stderrPrinter struct{}

// PrintError writes the fault's reason on one line.
func (stderrPrinter) PrintError(f fault.Fault) error {
	_, err := fmt.Fprintf(os.Stderr, "komodo: %s\n", f.Reason)
	return err
}

// defaultRecoverer guards goroutines of adapters built without a fault handler.
var defaultRecoverer Recoverer = fault.New(nil, stderrPrinter{})

// recoverer returns r, or the default when r is nil.
func recoverer(r Recoverer) Recoverer {
	if r != nil {
		return r
	}
	return defaultRecoverer
}

// llmSource names this package in a fault.
var llmSource = fault.Source{Component: "llm"}
