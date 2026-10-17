package llm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// StopLevel is one rung of the stop ladder.
type StopLevel int

// The rungs, gentlest first.
const (
	// ends the turn, keeps its result (API links: cancel after the current tool call)
	Interrupt StopLevel = iota
	Terminate           // SIGTERM
	Kill                // whole process tree
)

// Stream delivers one session's events and accepts injected lines.
type Stream interface {
	// Next blocks for the next event and returns io.EOF after the Result event.
	Next(ctx context.Context) (StreamEvent, error)
	// Inject sends one line to the live session: the time warning or the nudge.
	Inject(line string) error
	// Stop applies one rung of the stop ladder.
	Stop(level StopLevel) error
	// Close releases the process or connection; it is safe to call twice.
	Close() error
}

// StreamKind classifies a stream event.
type StreamKind string

// The event kinds.
const (
	KindInit      StreamKind = "init"       // first line of a claude program session
	KindOutput    StreamKind = "output"     // any other model output line or API chunk
	KindRetry     StreamKind = "retry"      // the claude program announced an API retry
	KindRateLimit StreamKind = "rate_limit" // the claude program reported a rate-limit event
	KindHeadroom  StreamKind = "headroom"   // rate-limit headers read on an API response
	KindResult    StreamKind = "result"     // the final event
)

// StreamEvent is one parsed stream line; exactly one payload field is set by Kind.
type StreamEvent struct {
	Kind      StreamKind
	Raw       json.RawMessage // the line as received, appended to stream.jsonl
	Init      *Init
	Retry     *Retry
	RateLimit *RateLimit
	Headroom  *Headroom
	Result    *Result
}

// Init carries the field the billing check reads from the first line.
type Init struct {
	APIKeySource string // "none" on a subscription login
}

// Retry is a retry announcement from the claude program.
type Retry struct {
	Category     string // rate_limit, overloaded, billing_error or max_output_tokens
	RetryDelayMS int    // the session counts as alive until this delay passes
}

// RateLimit is a rate-limit report from the claude program.
type RateLimit struct {
	IsUsingOverage bool      // true when a Max plan draws paid overage credits
	ResetAt        time.Time // when the cap lifts; zero when absent
	Type           string    // the program's window name, such as five_hour or seven_day
	// the program's status word, such as allowed or rejected; empty when absent
	Status string
}

// Headroom is one rate-limit bucket read from response headers.
type Headroom struct {
	Name      string
	Limit     int64
	Remaining int64
	Reset     time.Time
}

// Usage counts tokens and cost; always read.
type Usage struct {
	TokensIn   int     // tokens sent to the model
	TokensOut  int     // tokens the model returned
	CacheRead  int     // tokens served from the provider cache
	CacheWrite int     // tokens written to the provider cache
	USD        float64 // metered cost; zero on a subscription link
	USDEquiv   float64 // notional cost: the claude program's total_cost_usd on a subscription
}

// ResumeHandle names a saved session so a later Resume can continue it.
type ResumeHandle struct {
	Link  Link   // the link that made the handle
	Kind  string // "session_id" (claude program) or "messages_file" (API links)
	Value string // the session UUID, or the path of the saved messages file
}

// Result is what a session stream ends with.
type Result struct {
	Output json.RawMessage // the final answer, shaped by the agent's result schema
	Usage  *Usage
	Handle ResumeHandle
}

// EndError is returned by Next when the tool loop ends a session without a usable result; Reason is
// the session's exit reason.
type EndError struct {
	Reason state.Reason
	Detail string
}

// PercentLeft returns Remaining divided by Limit, times 100; zero when the limit is unknown.
func (h Headroom) PercentLeft() float64 {
	if h.Limit <= 0 {
		return 0
	}

	return float64(h.Remaining) / float64(h.Limit) * 100
}

// Error describes the exit reason and any detail.
func (e *EndError) Error() string {
	if e.Detail == "" {
		return "session ended: " + string(e.Reason)
	}

	return "session ended: " + string(e.Reason) + ": " + e.Detail
}
