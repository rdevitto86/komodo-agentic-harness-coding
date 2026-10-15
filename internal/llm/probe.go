package llm

import (
	"context"
	"encoding/json"
	"slices"
)

// ProbeResult is what Probe records for one link.
type ProbeResult struct {
	Link      Link
	OK        bool
	Reason    string            // why the link is unusable, when OK is false
	Path      string            // subscription link: absolute path of the claude program
	Version   string            // subscription link: program version
	Auth      AuthStatus        // subscription link: parsed `claude auth status --json`
	InitLine  json.RawMessage   // subscription link: one system/init line per program version
	Models    map[Weight]string // exact identifiers per weight
	Ctx       int               // measured context in tokens
	CtxSource string            // "models_api", "catalog", "default", "ollama_ps" or "endpoint"
	Ollama    bool              // local link: true when GET /api/version answered
}

// AuthStatus holds the two login fields the billing check reads.
type AuthStatus struct {
	AuthMethod       string `json:"authMethod"`       // "claude.ai" on a subscription login
	SubscriptionType string `json:"subscriptionType"` // pro, max, team or enterprise
}

// DefaultWindow is the context assumed for a model neither measured nor in the catalog.
const DefaultWindow = 262144

// ProbeAll probes each link and returns one result per link; a failing link is a result with OK false, never an error.
func (c *Client) ProbeAll(ctx context.Context) []ProbeResult {
	links := make([]Link, 0, len(c.providers))
	for l := range c.providers {
		links = append(links, l)
	}
	slices.Sort(links)
	out := make([]ProbeResult, 0, len(links))
	for _, l := range links {
		res, err := c.Probe(ctx, l)
		res.Link = l
		if err != nil {
			res.OK = false
			if res.Reason == "" {
				res.Reason = err.Error()
			}
		}
		out = append(out, res)
	}
	return out
}
