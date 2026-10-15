package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// chatWire sends one turn over the OpenAI-compatible chat interface.
type chatWire struct {
	p      *LocalProvider
	model  string
	system string
	tools  []ToolDef
	schema json.RawMessage
}

// newWire gathers what every request of one session repeats.
func (p *LocalProvider) newWire(s Session) *chatWire {
	w := &chatWire{p: p, model: s.Model, schema: s.Schema}
	if b, err := os.ReadFile(filepath.Join(s.Dir, "system.md")); err == nil {
		w.system = string(b)
	}
	w.tools = toolDefs(s.Tools)
	return w
}

// chatMessage is one message of the chat interface.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []chatCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// chatCall is one tool call of an assistant message.
type chatCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toChat maps the loop's messages to the chat interface's.
func toChat(system string, msgs []message) []chatMessage {
	var out []chatMessage
	if system != "" {
		out = append(out, chatMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			cm := chatMessage{Role: "assistant"}
			var text []string
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					text = append(text, b.Text)
				case "tool_use":
					c := chatCall{ID: b.ID, Type: "function"}
					c.Function.Name, c.Function.Arguments = b.Name, string(b.Input)
					cm.ToolCalls = append(cm.ToolCalls, c)
				}
			}
			cm.Content = strings.Join(text, "\n")
			out = append(out, cm)
		default:
			var text []string
			for _, b := range m.Content {
				switch b.Type {
				case "tool_result":
					out = append(out, chatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: b.Content})
				case "text":
					text = append(text, b.Text)
				}
			}
			if len(text) > 0 {
				out = append(out, chatMessage{Role: "user", Content: strings.Join(text, "\n\n")})
			}
		}
	}
	return out
}

// chatRequest is the body of POST /chat/completions.
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Stream         bool           `json:"stream"`
	StreamOptions  map[string]any `json:"stream_options,omitempty"`
	Tools          []chatTool     `json:"tools,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

// chatTool is one tool definition of the chat interface.
type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// buildRequest assembles the body; the final call sends no tools and asks for the schema as the response format.
func (w *chatWire) buildRequest(req turnRequest) ([]byte, error) {
	r := chatRequest{Model: w.model, Messages: toChat(w.system, req.Messages), Stream: true, StreamOptions: map[string]any{"include_usage": true}}
	if req.Final {
		if len(w.schema) > 0 {
			r.ResponseFormat = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "result", "strict": true, "schema": w.schema}}
		}
	} else {
		for _, t := range w.tools {
			ct := chatTool{Type: "function"}
			ct.Function.Name, ct.Function.Description, ct.Function.Parameters = t.Name, t.Description, t.InputSchema
			r.Tools = append(r.Tools, ct)
		}
	}
	return json.Marshal(r)
}

// turn sends the history, reads the streamed reply and assembles it.
func (w *chatWire) turn(ctx context.Context, req turnRequest, h turnHooks) (turnResult, error) {
	body, err := w.buildRequest(req)
	if err != nil {
		return turnResult{}, err
	}
	hr, err := w.p.request(ctx, http.MethodPost, w.p.base+"/chat/completions", body)
	if err != nil {
		return turnResult{}, err
	}
	acc := &chatAcc{}
	resp, err := w.p.hc.Stream(ctx, w.p.ep, hr, core.StreamHooks{
		Activity: h.Activity, Event: acc.event, Restart: acc.reset,
		Final: func(e core.SSEEvent) bool { return strings.TrimSpace(e.Data) == "[DONE]" },
	})
	if err != nil {
		return turnResult{}, err
	}
	res := acc.result()
	for _, x := range resp.Headroom {
		res.Headroom = append(res.Headroom, Headroom{Name: x.Name, Limit: x.Limit, Remaining: x.Remaining, Reset: x.Reset})
	}
	return res, nil
}

// chatAcc assembles one streamed chat reply.
type chatAcc struct {
	text   strings.Builder
	calls  map[int]*chatCall
	finish string
	usage  Usage
}

// reset drops partial output before a retry.
func (a *chatAcc) reset() { *a = chatAcc{} }

// event folds one chunk into the reply.
func (a *chatAcc) event(e core.SSEEvent) error {
	if strings.TrimSpace(e.Data) == "[DONE]" {
		return nil
	}
	var c struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(e.Data), &c) != nil {
		return nil
	}
	if c.Usage != nil {
		a.usage = Usage{TokensIn: c.Usage.Prompt, TokensOut: c.Usage.Completion}
	}
	for _, ch := range c.Choices {
		a.text.WriteString(ch.Delta.Content)
		for _, tc := range ch.Delta.ToolCalls {
			if a.calls == nil {
				a.calls = map[int]*chatCall{}
			}
			call := a.calls[tc.Index]
			if call == nil {
				call = &chatCall{Type: "function"}
				a.calls[tc.Index] = call
			}
			if tc.ID != "" {
				call.ID = tc.ID
			}
			call.Function.Name += tc.Function.Name
			call.Function.Arguments += tc.Function.Arguments
		}
		if ch.Finish != "" {
			a.finish = ch.Finish
		}
	}
	return nil
}

// result returns the assembled reply with the finish reason mapped to the loop's.
func (a *chatAcc) result() turnResult {
	res := turnResult{Usage: a.usage, Stop: stopEnd}
	if t := a.text.String(); t != "" {
		res.Content = append(res.Content, block{Type: "text", Text: t})
	}
	idx := make([]int, 0, len(a.calls))
	for i := range a.calls {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	for _, i := range idx {
		c := a.calls[i]
		in := strings.TrimSpace(c.Function.Arguments)
		if in == "" {
			in = "{}"
		}
		invalid := a.finish == "length" || !json.Valid([]byte(in))
		if invalid {
			in = "{}"
		}
		id := c.ID
		if id == "" {
			id = "call_" + strings.Repeat("0", 3) + string(rune('a'+i%26))
		}
		res.Content = append(res.Content, block{Type: "tool_use", ID: id, Name: c.Function.Name, Input: json.RawMessage(in), Invalid: invalid})
	}
	switch {
	case a.finish == "length":
		res.Stop = stopMaxTokens
	case len(a.calls) > 0:
		res.Stop = stopToolUse
	}
	return res
}
