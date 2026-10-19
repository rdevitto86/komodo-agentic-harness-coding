package engine

import (
	"context"
	"encoding/json"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// verbToolList answers a session's tool server with the tools that session may see.
func (e *Engine) verbToolList(w *connWriter, req Request) {
	if req.Tool == nil || req.Tool.Session == "" {
		e.badFrame(w, req.ID, "tool_list needs a session")
		return
	}

	defs, ok := e.toolHub().Definitions(req.Tool.Session)
	if !ok {
		e.unknownSession(w, req)
		return
	}

	e.done(w, req.ID, ToolList{Tools: defs})
}

// verbToolCall runs one tool call of a session behind that session's guard; the call ends when its connection closes.
func (e *Engine) verbToolCall(ctx context.Context, w *connWriter, req Request) {
	if req.Tool == nil || req.Tool.Session == "" || req.Tool.Name == "" {
		e.badFrame(w, req.ID, "tool_call needs a session and a tool name")
		return
	}

	text, ok := e.toolHub().Call(ctx, req.Tool.Session, req.Tool.Name, req.Tool.Args)
	if !ok {
		e.unknownSession(w, req)
		return
	}

	e.done(w, req.ID, ToolAnswer{Text: text})
}

// unknownSession refuses a tool request whose session is not running.
func (e *Engine) unknownSession(w *connWriter, req Request) {
	_ = w.send(Reply{
		ID:     req.ID,
		Status: StatusRefused,
		Code:   CodeUnknownSession,
		Detail: "no running session " + req.Tool.Session,
		Final:  true,
	})
}

// toolHub returns the engine's tool hub, or one with no sessions when none was wired.
func (e *Engine) toolHub() ToolHub {
	if e.d.Tools == nil {
		return noTools{}
	}
	return e.d.Tools
}

// noTools is the hub of an engine wired with none: no session runs.
type noTools struct{}

// Definitions reports no session.
func (noTools) Definitions(string) ([]llm.ToolDef, bool) { return nil, false }

// Call reports no session.
func (noTools) Call(context.Context, string, string, json.RawMessage) (string, bool) {
	return "", false
}
