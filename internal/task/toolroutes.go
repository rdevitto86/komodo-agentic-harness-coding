package task

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// RoutedTools is the bound tool set of one session: what the engine runs a relayed call on.
type RoutedTools interface {
	llm.ToolHost
	llm.ToolDefiner
}

// ToolRoutes holds the bound tools of every running subscription session, so the engine can run the calls that the
// session's tool server relays to it.
type ToolRoutes struct {
	mu       sync.Mutex
	sessions map[string]RoutedTools
}

// NewToolRoutes returns a route table with no session.
func NewToolRoutes() *ToolRoutes { return &ToolRoutes{sessions: map[string]RoutedTools{}} }

// Add routes a session's tool calls to its bound tools and returns the call that removes the route.
func (r *ToolRoutes) Add(session string, w RoutedTools) (remove func()) {
	r.mu.Lock()
	r.sessions[session] = w
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		if r.sessions[session] == w {
			delete(r.sessions, session)
		}
	}
}

// Definitions describes the tools the session may see; false when no such session runs.
func (r *ToolRoutes) Definitions(session string) ([]llm.ToolDef, bool) {
	w, ok := r.find(session)
	if !ok {
		return nil, false
	}

	defs := []llm.ToolDef{}

	for _, n := range w.Advertised() {
		if d, ok := w.Definition(n); ok {
			defs = append(defs, d)
		}
	}

	return defs, true
}

// Call runs one tool call through the session's guard and tools and returns the reply text; false when no such session
// runs.
func (r *ToolRoutes) Call(ctx context.Context, session, name string, args json.RawMessage) (string, bool) {
	w, ok := r.find(session)
	if !ok {
		return "", false
	}

	return w.Dispatch(ctx, name, args).Text, true
}

// find returns the tools routed for a session.
func (r *ToolRoutes) find(session string) (RoutedTools, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.sessions[session]
	return w, ok
}
