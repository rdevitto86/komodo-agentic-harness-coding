package engine

import "github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"

// faultsOf returns h, or a handler that publishes nothing when none was given.
func faultsOf(h *fault.Handler) *fault.Handler {
	if h == nil {
		return fault.New(nil, nil)
	}

	return h
}

// guarded runs fn on a new goroutine and reports its panic through h instead of ending the process.
func guarded(h *fault.Handler, fn func()) {
	go func() {
		defer h.Recover(fault.Source{Component: "engine"}, nil)

		fn()
	}()
}
