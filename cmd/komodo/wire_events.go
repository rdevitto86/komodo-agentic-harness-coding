package main

import (
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cleanup"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// The progress facts the Commit and Cleanup handlers report are published as run-status events; each wrapper binds the
// handler's own fact struct to its subtype.
type (
	publishDoneEvent struct{ commit.Result }
	prOpenedEvent    struct{ commit.Result }
	cleanupStartEvt  struct{ cleanup.StartedFact }
	cleanupDoneEvt   struct{ cleanup.Report }
	discardedEvt     struct{ cleanup.DiscardedFact }
)

// EventKey binds the wrapper to run-status / publish.done.
func (publishDoneEvent) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "publish.done"}
}

// EventKey binds the wrapper to run-status / pr.opened.
func (prOpenedEvent) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "pr.opened"}
}

// EventKey binds the wrapper to run-status / cleanup.start.
func (cleanupStartEvt) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "cleanup.start"}
}

// EventKey binds the wrapper to run-status / cleanup.done.
func (cleanupDoneEvt) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "cleanup.done"}
}

// EventKey binds the wrapper to run-status / group.discarded.
func (discardedEvt) EventKey() bus.Key {
	return bus.Key{Type: bus.TypeRunStatus, Subtype: "group.discarded"}
}

// registerHandlerEvents binds the handlers' progress facts to the event registry.
func registerHandlerEvents() {
	bus.Register(publishDoneEvent{}.EventKey(), func() bus.Payload { return &publishDoneEvent{} })
	bus.Register(prOpenedEvent{}.EventKey(), func() bus.Payload { return &prOpenedEvent{} })
	bus.Register(cleanupStartEvt{}.EventKey(), func() bus.Payload { return &cleanupStartEvt{} })
	bus.Register(cleanupDoneEvt{}.EventKey(), func() bus.Payload { return &cleanupDoneEvt{} })
	bus.Register(discardedEvt{}.EventKey(), func() bus.Payload { return &discardedEvt{} })
}
