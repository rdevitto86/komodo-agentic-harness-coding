// Package llm is the model client: one adapter per link, a tool loop for the HTTP links, and the
// model catalog.
package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
)

// Weight is the agent's size class.
type Weight string

// The size classes; heavy runs the Opus line, standard the Sonnet line, light the Haiku line.
const (
	Heavy    Weight = "heavy"
	Standard Weight = "standard"
	Light    Weight = "light"
)

// Link names one way to reach a model.
type Link string

// The links; the two OpenAI links are named in the interface and not built.
const (
	// the Claude subscription, through the claude program
	ClaudeCLI    Link = "claude-cli"
	AnthropicAPI Link = "anthropic-api" // the paid Anthropic HTTP interface
	// OpenAI's subscription; in the interface, not built
	OpenAISubscription Link = "openai-subscription"
	// OpenAI's paid HTTP interface; in the interface, not built
	OpenAIAPI Link = "openai-api"
	Local     Link = "local" // the optional free model on this machine
)

// Session is one model session an adapter runs.
type Session struct {
	ID      string          // session UUID chosen by the Task Manager before the start
	Dir     string          // the session folder: brief.md, system.md, mcp.json, out/
	Agent   string          // agent role, such as "builder"
	Link    Link            // the selected link
	Model   string          // exact pinned model identifier for the weight on this link
	Profile CapacityProfile // context, slots, cost class and effort measured for this session
	Schema  json.RawMessage // the agent's result schema for the final call
	Tools   ToolHost        // bound tool functions; used by API links only
	Env     []string        // environment of the claude program, after the scrub
	Cache   cache.Handle    // the cache handle; the paid adapter stores its marker plan there
	Parts   [5]string       // the brief's five parts; API links place cache markers between them
	// the role's own check of a result; API links only, nil accepts any valid result
	Gate Gate
}

// Input is the one new user message sent when a session resumes.
type Input struct {
	Text string // written to standard input (claude-cli) or appended as the next turn (API links)
}

// CapacityProfile is what the model client measures for one link and weight.
type CapacityProfile struct {
	Ctx       int    // context window in tokens, measured
	Slots     int    // concurrent sessions allowed on the link
	CostClass string // subscription, free or paid
	Effort    string // high, medium or low
}

// ToolReply is what the model receives for one tool call.
type ToolReply struct {
	Text    string // at most 4 KB, except for read
	LogPath string // out/<call>.log; set when Text was capped
}

// ToolHost is the bound tool set the API-link loop calls; task.Bound satisfies it.
type ToolHost interface {
	// Advertised returns the names of the tools sent with each request.
	Advertised() []string
	// Dispatch runs the guard, then the tool, and returns a capped reply.
	Dispatch(ctx context.Context, name string, args json.RawMessage) ToolReply
}

// ToolDef is one tool's definition as the API links send it.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolDefiner is implemented by a ToolHost that can describe its tools; a host without it gets a
// permissive schema.
type ToolDefiner interface {
	// Definition returns the definition of one advertised tool.
	Definition(name string) (ToolDef, bool)
}

// Gate is the role's own check of a session result.
type Gate interface {
	// Check returns an empty string when the result passes, else the text sent back into the
	// session.
	Check(ctx context.Context, output json.RawMessage) (string, error)
}

// Provider is implemented once per link; the Task Manager treats every link the same way.
type Provider interface {
	// Link returns the link this adapter serves.
	Link() Link
	// Probe checks the link is reachable and records its path, version and login; no model turn.
	Probe(ctx context.Context) (ProbeResult, error)
	// Profile measures the link's capacity profile for a weight.
	Profile(ctx context.Context, w Weight) (CapacityProfile, error)
	// Start begins one model session and returns the stream of its events.
	Start(ctx context.Context, s Session) (Stream, error)
	// Resume continues a saved session from its handle, sending the new input.
	Resume(ctx context.Context, s Session, in Input) (Stream, error)
}

// Client holds the adapters by link name.
type Client struct {
	providers map[Link]Provider
}

// The cost classes a link can have.
const (
	CostSubscription = "subscription"
	CostFree         = "free"
	CostPaid         = "paid"
)

// New builds a Client over the adapters that this machine supports.
func New(providers map[Link]Provider) *Client {
	m := make(map[Link]Provider, len(providers))
	for l, p := range providers {
		m[l] = p
	}

	return &Client{providers: m}
}

// Probe dispatches to the adapter of the named link.
func (c *Client) Probe(ctx context.Context, l Link) (ProbeResult, error) {
	p, err := c.provider(l)
	if err != nil {
		return ProbeResult{Link: l, Reason: err.Error()}, err
	}

	return p.Probe(ctx)
}

// Profile dispatches to the adapter of the named link.
func (c *Client) Profile(ctx context.Context, l Link, w Weight) (CapacityProfile, error) {
	p, err := c.provider(l)
	if err != nil {
		return CapacityProfile{}, err
	}

	return p.Profile(ctx, w)
}

// Start dispatches to the adapter of the session's link.
func (c *Client) Start(ctx context.Context, s Session) (Stream, error) {
	p, err := c.provider(s.Link)
	if err != nil {
		return nil, err
	}

	return p.Start(ctx, s)
}

// Resume dispatches to the adapter of the session's link.
func (c *Client) Resume(ctx context.Context, s Session, in Input) (Stream, error) {
	p, err := c.provider(s.Link)
	if err != nil {
		return nil, err
	}

	return p.Resume(ctx, s, in)
}

// provider returns the adapter of a link, ErrNotYet for an OpenAI link and ErrUnavailable for any
// other missing one.
func (c *Client) provider(l Link) (Provider, error) {
	if p, ok := c.providers[l]; ok {
		return p, nil
	}

	switch l {
	case OpenAISubscription, OpenAIAPI:
		return nil, fmt.Errorf("%s: %w", l, ErrNotYet)
	}

	return nil, fmt.Errorf("%s: %w", l, ErrUnavailable)
}
