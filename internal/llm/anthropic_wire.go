package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// maxExplicitMarkers is how many explicit cache markers fit beside the automatic one.
const maxExplicitMarkers = 3

// anthropicWire sends one turn over the Messages interface.
type anthropicWire struct {
	a      *Anthropic
	model  string
	system string
	tools  []ToolDef
	effort string
	schema json.RawMessage
	max    int
}

// newWire gathers what every request of one session repeats: model, system text, tool definitions and effort.
func (a *Anthropic) newWire(s Session) (*anthropicWire, error) {
	w := &anthropicWire{a: a, model: s.Model, schema: s.Schema, effort: s.Profile.Effort, max: a.opts.MaxTokens}
	if w.effort == "" {
		w.effort = "medium"
	}
	if w.max <= 0 {
		w.max = defaultMaxOut
		if e, ok := a.cat.Lookup(s.Model); ok && e.MaxOut > 0 {
			w.max = min(w.max, e.MaxOut)
		}
	}
	if b, err := os.ReadFile(filepath.Join(s.Dir, "system.md")); err == nil {
		w.system = string(b)
	}
	w.tools = toolDefs(s.Tools)
	return w, nil
}

// apiRequest is the body of POST /v1/messages.
type apiRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	Stream       bool          `json:"stream"`
	System       []block       `json:"system,omitempty"`
	Tools        []ToolDef     `json:"tools,omitempty"`
	Messages     []message     `json:"messages"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
	OutputConfig *apiOutputCfg `json:"output_config,omitempty"`
}

// apiOutputCfg carries effort and, on the final call, the result schema.
type apiOutputCfg struct {
	Effort string     `json:"effort,omitempty"`
	Format *apiFormat `json:"format,omitempty"`
}

// apiFormat is the structured-output request.
type apiFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// takesEffort reports whether a model accepts the effort setting.
func takesEffort(model string) bool { return !strings.HasPrefix(model, "claude-haiku-4") }

// countMarkers counts the explicit cache markers in a request.
func countMarkers(r apiRequest) int {
	n := 0
	for _, b := range r.System {
		if b.CacheControl != nil {
			n++
		}
	}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

// buildRequest assembles the request body; it fails before sending when the markers would not fit.
func (w *anthropicWire) buildRequest(req turnRequest) ([]byte, error) {
	r := apiRequest{Model: w.model, MaxTokens: w.max, Stream: true, Tools: w.tools, Messages: req.Messages,
		CacheControl: &cacheControl{Type: "ephemeral"}}
	if w.system != "" {
		r.System = []block{{Type: "text", Text: w.system}}
	}
	if takesEffort(w.model) {
		r.OutputConfig = &apiOutputCfg{Effort: w.effort}
	}
	if req.Final && len(w.schema) > 0 {
		if r.OutputConfig == nil {
			r.OutputConfig = &apiOutputCfg{}
		}
		r.OutputConfig.Format = &apiFormat{Type: "json_schema", Schema: w.schema}
	}
	if n := countMarkers(r); n > maxExplicitMarkers {
		return nil, fmt.Errorf("request carries %d explicit cache markers; at most %d fit beside the automatic one", n, maxExplicitMarkers)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return b, nil
}

// turn sends the history, reads the streamed reply and assembles it.
func (w *anthropicWire) turn(ctx context.Context, req turnRequest, h turnHooks) (turnResult, error) {
	body, err := w.buildRequest(req)
	if err != nil {
		return turnResult{}, err
	}
	hr, err := w.a.request(ctx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		return turnResult{}, err
	}
	acc := &sseAcc{}
	resp, err := w.a.hc.Stream(ctx, w.a.ep, hr, core.StreamHooks{
		Activity: h.Activity, Event: acc.event, Restart: acc.reset,
		Final: func(e core.SSEEvent) bool { return e.Event == "message_stop" },
	})
	if err != nil {
		return turnResult{}, normalizeAPIErr(err)
	}
	res := acc.result()
	for _, x := range resp.Headroom {
		res.Headroom = append(res.Headroom, Headroom{Name: x.Name, Limit: x.Limit, Remaining: x.Remaining, Reset: x.Reset})
	}
	return res, nil
}

// specLimitPrefix begins the message of a 400 that reports a spend limit set by the account owner.
const specLimitPrefix = "You have reached your specified API usage limits"

// normalizeAPIErr turns a 400 spend-limit reply into the limit error the loop maps to a provider limit.
func normalizeAPIErr(err error) error {
	var se *core.StatusError
	if errors.As(err, &se) && se.Status == 400 {
		var v struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(se.Body, &v) == nil && strings.HasPrefix(v.Error.Message, specLimitPrefix) {
			return &core.LimitError{Kind: core.LimitSpend, Status: 400}
		}
	}
	return err
}

// sseAcc assembles content blocks from the event stream of one reply.
type sseAcc struct {
	blocks  map[int]*block
	partial map[int]*bytes.Buffer
	usage   Usage
	stop    string
}

// reset drops partial output before a retry.
func (a *sseAcc) reset() { *a = sseAcc{} }

// event folds one server-sent event into the reply.
func (a *sseAcc) event(e core.SSEEvent) error {
	if a.blocks == nil {
		a.blocks, a.partial = map[int]*block{}, map[int]*bytes.Buffer{}
	}
	var p struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Block *block `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Message struct {
			Usage apiUsage `json:"usage"`
		} `json:"message"`
		Usage apiUsage `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(e.Data), &p); err != nil {
		return nil // keep-alive or unknown payload
	}
	switch p.Type {
	case "message_start":
		u := p.Message.Usage
		a.usage = Usage{TokensIn: u.Input, TokensOut: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
	case "content_block_start":
		if p.Block != nil {
			b := *p.Block
			a.blocks[p.Index] = &b
			a.partial[p.Index] = &bytes.Buffer{}
		}
	case "content_block_delta":
		b := a.blocks[p.Index]
		if b == nil {
			return nil
		}
		switch p.Delta.Type {
		case "text_delta":
			b.Text += p.Delta.Text
		case "thinking_delta":
			b.Thinking += p.Delta.Thinking
		case "signature_delta":
			b.Signature += p.Delta.Signature
		case "input_json_delta":
			a.partial[p.Index].WriteString(p.Delta.PartialJSON)
		}
	case "message_delta":
		if p.Delta.StopReason != "" {
			a.stop = p.Delta.StopReason
		}
		if p.Usage.Output > 0 {
			a.usage.TokensOut = p.Usage.Output
		}
	case "error":
		return fmt.Errorf("provider error %s: %s", p.Error.Type, p.Error.Message)
	}
	return nil
}

// apiUsage is the usage object of the Messages interface.
type apiUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

// result returns the assembled reply with the stop reason mapped to the loop's.
func (a *sseAcc) result() turnResult {
	idx := make([]int, 0, len(a.blocks))
	for i := range a.blocks {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	res := turnResult{Usage: a.usage, Stop: stopEnd}
	for _, i := range idx {
		b := *a.blocks[i]
		if b.Type == "tool_use" {
			in := bytes.TrimSpace(a.partial[i].Bytes())
			if len(in) == 0 || !json.Valid(in) {
				in = []byte("{}")
			}
			b.Input = json.RawMessage(append([]byte(nil), in...))
		}
		res.Content = append(res.Content, b)
	}
	switch a.stop {
	case "tool_use":
		res.Stop = stopToolUse
	case "max_tokens":
		res.Stop = stopMaxTokens
	}
	return res
}

// toolDefs lists the host's advertised tools by name, with definitions when the host can give them.
func toolDefs(host ToolHost) []ToolDef {
	if host == nil {
		return nil
	}
	names := slices.Clone(host.Advertised())
	slices.Sort(names)
	def, _ := host.(ToolDefiner)
	var out []ToolDef
	for _, n := range names {
		d := ToolDef{Name: n, Description: n}
		if def != nil {
			if got, ok := def.Definition(n); ok {
				d = got
				d.Name = n
			}
		}
		if len(d.InputSchema) == 0 {
			d.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, d)
	}
	return out
}
