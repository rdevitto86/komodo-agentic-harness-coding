package llm

import (
	"context"
	"encoding/json"
	"time"
)

// cacheControl is a provider cache marker on a block.
type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// block is one content block of a message, in the Messages interface's shape; the local adapter maps it to its own.
type block struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

// message is one turn of the conversation.
type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// The reasons a model turn stopped.
const (
	stopEnd       = "end"
	stopToolUse   = "tool_use"
	stopMaxTokens = "max_tokens"
)

// turnRequest is what the loop asks a wire to send.
type turnRequest struct {
	Messages []message
	Final    bool // the one call that returns the result schema
}

// turnResult is one model turn as the loop sees it.
type turnResult struct {
	Content  []block
	Stop     string
	Usage    Usage
	Headroom []Headroom
}

// turnHooks lets a wire report liveness and output while a turn streams.
type turnHooks struct {
	Activity func() // bytes arrived
}

// wire sends one turn over one interface and returns the assembled reply.
type wire interface {
	turn(ctx context.Context, req turnRequest, h turnHooks) (turnResult, error)
}

// loopConfig is everything the tool loop needs for one session.
type loopConfig struct {
	Session  Session
	Wire     wire
	Link     Link
	Messages []message           // the history to start from; the loop writes it to messages.jsonl
	Price    func(Usage) float64 // metered cost of a turn; nil means zero
	Now      func() time.Time
	Faults   Recoverer // guards the loop goroutine; nil prints a panic on standard error
}
