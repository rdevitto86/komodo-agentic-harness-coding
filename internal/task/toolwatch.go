package task

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// toolWatch counts the tool calls of one session: in flight, last activity and refusals per guard row. It wraps the bound
// tools, whether the API link's loop or the engine on behalf of a subscription session's tool server calls them.
type toolWatch struct {
	inner   *Bound
	limit   int
	now     func() time.Time
	mu      sync.Mutex
	running int
	last    time.Time
	halt    Reason
}

var (
	_ llm.ToolHost    = (*toolWatch)(nil)
	_ llm.ToolDefiner = (*toolWatch)(nil)
)

// Advertised returns the names of the tools the session may see.
func (w *toolWatch) Advertised() []string { return w.inner.Advertised() }

// Definition describes one advertised tool.
func (w *toolWatch) Definition(name string) (llm.ToolDef, bool) { return w.inner.Definition(name) }

// Dispatch runs one call and counts it while it runs.
func (w *toolWatch) Dispatch(ctx context.Context, name string, args json.RawMessage) Reply {
	w.touch(1)
	defer w.touch(-1)
	return w.inner.Dispatch(ctx, name, args)
}

// Tools returns the calls in flight and the last tool activity.
func (w *toolWatch) Tools() (int, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running, w.last
}

// Halt returns the reason the session must stop for, or empty.
func (w *toolWatch) Halt() Reason {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.halt != "" {
		return w.halt
	}
	if w.inner != nil {
		for _, n := range w.inner.Guard().Refusals() {
			if w.limit > 0 && n >= w.limit {
				return state.ReasonRefusals
			}
		}
	}

	return ""
}

// Refusals returns the refusals counted per guard row in this session.
func (w *toolWatch) Refusals() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := map[string]int{}
	if w.inner != nil {
		for k, v := range w.inner.Guard().Refusals() {
			out[string(k)] = v
		}
	}

	return out
}

// touch changes the in-flight count and stamps the activity time.
func (w *toolWatch) touch(delta int) {
	w.mu.Lock()
	w.running += delta
	w.last = w.now()
	w.mu.Unlock()
}

// stop makes Halt return a reason.
func (w *toolWatch) stop(r Reason) {
	w.mu.Lock()
	w.halt = r
	w.mu.Unlock()
}
