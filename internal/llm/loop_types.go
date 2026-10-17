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

// block is one content block of a message, in the Messages interface's shape; the local adapter
// maps it to its own.
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
	// a tool_use whose arguments were cut off or not valid JSON; it is answered, never run
	Invalid bool `json:"-"`
}

// plainBlock has block's fields and none of its methods, so a fallback encode cannot recurse.
type plainBlock block

// message is one turn of the conversation.
type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

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

	CacheWrite1h int // of Usage.CacheWrite, the tokens written with the one-hour lifetime
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
	Session Session
	Wire    wire
	Link    Link
	// the history to start from; the loop writes it to messages.jsonl
	Messages []message
	Price    func(u Usage, write1h int) float64 // metered cost of a turn; nil means zero
	Now      func() time.Time
	Faults   Recoverer // guards the loop goroutine; nil prints a panic on standard error
}

// The reasons a model turn stopped.
const (
	stopEnd       = "end"
	stopToolUse   = "tool_use"
	stopMaxTokens = "max_tokens"
)

// MarshalJSON writes the fields the Messages interface requires for each block type, even when
// empty.
func (b block) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case "text":
		return json.Marshal(struct {
			Type         string        `json:"type"`
			Text         string        `json:"text"`
			CacheControl *cacheControl `json:"cache_control,omitempty"`
		}{b.Type, b.Text, b.CacheControl})
	case "thinking":
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{b.Type, b.Thinking, b.Signature})
	case "redacted_thinking":
		return json.Marshal(struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}{b.Type, b.Data})
	case "tool_use":
		in := b.Input
		if len(in) == 0 {
			in = json.RawMessage("{}")
		}

		return json.Marshal(struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{b.Type, b.ID, b.Name, in})
	case "tool_result":
		return json.Marshal(struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
		}{b.Type, b.ToolUseID, b.Content})
	}

	return json.Marshal(plainBlock(b))
}
