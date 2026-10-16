package task

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// toolWatch counts the tool calls of one session: in flight, last activity and refusals per guard row.
// On an API link it wraps the bound tools; on the subscription link it follows tools.jsonl.
type toolWatch struct {
	inner   *Bound
	dir     string
	limit   int
	now     func() time.Time
	onFact  func(Fact)
	onFind  func(Submitted)
	mu      sync.Mutex
	started map[string]bool
	running int
	last    time.Time
	denied  map[string]int
	halt    Reason
	offset  int64
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

// touch changes the in-flight count and stamps the activity time.
func (w *toolWatch) touch(delta int) {
	w.mu.Lock()
	w.running += delta
	w.last = w.now()
	w.mu.Unlock()
}

// Tools returns the calls in flight and the last tool activity.
func (w *toolWatch) Tools() (int, time.Time) {
	w.poll()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running, w.last
}

// Halt returns the reason the session must stop for, or empty.
func (w *toolWatch) Halt() Reason {
	w.poll()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.halt != "" {
		return w.halt
	}
	for _, n := range w.denied {
		if w.limit > 0 && n >= w.limit {
			return state.ReasonRefusals
		}
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
	for k, v := range w.denied {
		out[k] = v
	}
	if w.inner != nil {
		for k, v := range w.inner.Guard().Refusals() {
			out[string(k)] = v
		}
	}
	return out
}

// stop makes Halt return a reason.
func (w *toolWatch) stop(r Reason) {
	w.mu.Lock()
	w.halt = r
	w.mu.Unlock()
}

// poll reads the lines tools.jsonl gained since the last poll; an API link has none to follow.
func (w *toolWatch) poll() {
	if w.dir == "" || w.inner != nil {
		return
	}
	f, err := os.Open(filepath.Join(w.dir, "tools.jsonl"))
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := f.Seek(w.offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return
	}
	w.offset += int64(end + 1)
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		var tl ToolLine
		if json.Unmarshal(line, &tl) != nil {
			continue
		}
		w.last = w.now()
		w.apply(tl)
	}
}

// apply folds one tool line into the counts and hands denied and finding lines on as facts.
func (w *toolWatch) apply(tl ToolLine) {
	switch tl.Kind {
	case "start":
		if w.started == nil {
			w.started = map[string]bool{}
		}
		w.started[tl.Call] = true
		w.running++
	case "end":
		if w.started[tl.Call] {
			delete(w.started, tl.Call)
			w.running--
		}
	case "denied":
		var d struct {
			Row   string `json:"row"`
			Count int    `json:"count"`
		}
		_ = json.Unmarshal(tl.Data, &d)
		if w.denied == nil {
			w.denied = map[string]int{}
		}
		w.denied[d.Row] = max(w.denied[d.Row], d.Count)
		if w.onFact != nil {
			w.onFact(Fact{Subtype: "tool.denied", At: w.now(), Data: tl.Data})
		}
	case "finding":
		if w.onFact != nil {
			w.onFact(Fact{Subtype: "finding.submit", At: w.now(), Data: tl.Data})
		}
		var s struct {
			Rule  string `json:"rule"`
			File  string `json:"file"`
			Line  int    `json:"line"`
			Quote string `json:"quote"`
			Note  string `json:"note"`
		}
		if json.Unmarshal(tl.Data, &s) == nil && w.onFind != nil {
			f := Submitted{Note: s.Note}
			f.Rule, f.File, f.Line, f.Quote = s.Rule, s.File, s.Line, s.Quote
			w.onFind(f)
		}
	}
}
