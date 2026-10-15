package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// Defaults of the paid Anthropic link.
const (
	anthropicBase    = "https://api.anthropic.com"
	anthropicVersion = "2023-06-01"
	anthropicSlots   = 3
	defaultMaxOut    = 64000
)

// AnthropicOptions tunes the paid Anthropic adapter; Key is the only required field.
type AnthropicOptions struct {
	BaseURL   string           // server address; empty means the public interface
	Key       string           // the API key's value, resolved by the caller from its env: reference
	HTTP      *core.Client     // shared HTTP client; nil builds one that redacts the key
	Version   string           // anthropic-version header; empty means 2023-06-01
	Slots     int              // concurrent sessions allowed on the link; 0 means 3
	MaxTokens int              // output cap per request; 0 means 64,000 or the model's cap if lower
	Catalog   *Catalog         // model catalog; nil loads the embedded one
	Now       func() time.Time // clock; nil uses time.Now
}

// Anthropic is the paid HTTP adapter for the Messages interface.
type Anthropic struct {
	opts AnthropicOptions
	cat  *Catalog
	hc   *core.Client
	ep   core.Endpoint
	base string
}

// NewAnthropic builds the paid adapter and its HTTP client.
func NewAnthropic(opts AnthropicOptions) (*Anthropic, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = anthropicBase
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("anthropic base URL %q is not a URL", opts.BaseURL)
	}
	a := &Anthropic{opts: opts, cat: opts.Catalog, hc: opts.HTTP, base: base}
	if a.cat == nil {
		a.cat, _ = LoadCatalog()
	}
	if a.hc == nil {
		if a.hc, err = core.NewClient(core.Options{Secrets: []string{opts.Key}}); err != nil {
			return nil, err
		}
	} else if opts.Key != "" {
		a.hc.AddSecrets(opts.Key)
	}
	a.ep = core.Endpoint{Hosts: []string{u.Host}, Methods: []string{http.MethodGet, http.MethodPost}}
	return a, nil
}

// Link returns AnthropicAPI.
func (a *Anthropic) Link() Link { return AnthropicAPI }

// now returns the adapter's clock.
func (a *Anthropic) now() time.Time {
	if a.opts.Now != nil {
		return a.opts.Now()
	}
	return time.Now()
}

// request builds one authenticated request.
func (a *Anthropic) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	if rd != nil {
		req, err = http.NewRequestWithContext(ctx, method, a.base+path, rd)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, a.base+path, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	ver := a.opts.Version
	if ver == "" {
		ver = anthropicVersion
	}
	req.Header.Set("x-api-key", a.opts.Key)
	req.Header.Set("anthropic-version", ver)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// modelInfo is one entry of the Models interface.
type modelInfo struct {
	ID             string `json:"id"`
	MaxInputTokens int    `json:"max_input_tokens"`
}

// listModels reads the models the key can use.
func (a *Anthropic) listModels(ctx context.Context) ([]modelInfo, error) {
	req, err := a.request(ctx, http.MethodGet, "/v1/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.hc.Do(ctx, a.ep, req)
	if err != nil {
		return nil, classifyProbeErr(err)
	}
	var out struct {
		Data []modelInfo `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("parse the model list: %w", ErrUnavailable)
	}
	return out.Data, nil
}

// classifyProbeErr maps an HTTP failure of a probe to ErrUnauthorized or ErrUnavailable.
func classifyProbeErr(err error) error {
	var se *core.StatusError
	if errors.As(err, &se) && (se.Status == 401 || se.Status == 403) {
		return fmt.Errorf("the key was refused (HTTP %d): %w", se.Status, ErrUnauthorized)
	}
	return fmt.Errorf("%v: %w", err, ErrUnavailable)
}

// Probe lists the key's models and picks the identifier of each weight.
func (a *Anthropic) Probe(ctx context.Context) (ProbeResult, error) {
	res := ProbeResult{Link: AnthropicAPI, Models: map[Weight]string{}}
	if a.opts.Key == "" {
		res.Reason = "no API key is set"
		return res, fmt.Errorf("%s: %w", res.Reason, ErrUnauthorized)
	}
	list, err := a.listModels(ctx)
	if err != nil {
		res.Reason = err.Error()
		return res, err
	}
	have := map[string]modelInfo{}
	for _, m := range list {
		have[m.ID] = m
	}
	least, measured := 0, true
	for _, w := range weightOrder {
		latest, ok := a.cat.Default(w)
		if !ok {
			continue
		}
		pick, found := have[latest.ID]
		if !found {
			if prior, ok := a.cat.Prior(latest.ID); ok {
				pick, found = have[prior.ID]
			}
		}
		if !found {
			continue
		}
		res.Models[w] = pick.ID
		win := pick.MaxInputTokens
		if win <= 0 {
			measured = false
			win, _ = windowFor(a.cat, pick.ID)
		}
		if least == 0 || win < least {
			least = win
		}
	}
	if len(res.Models) == 0 {
		res.Reason = "the key can use none of the catalog's models"
		return res, fmt.Errorf("%s: %w", res.Reason, ErrUnavailable)
	}
	res.Ctx, res.CtxSource = least, "models_api"
	if !measured {
		res.CtxSource = "catalog"
	}
	res.OK = true
	return res, nil
}

// Profile measures the context of a weight's default model from the Models interface, else the catalog.
func (a *Anthropic) Profile(ctx context.Context, w Weight) (CapacityProfile, error) {
	if a.opts.Key == "" {
		return CapacityProfile{}, fmt.Errorf("no API key is set: %w", ErrUnauthorized)
	}
	slots := a.opts.Slots
	if slots <= 0 {
		slots = anthropicSlots
	}
	p := CapacityProfile{Ctx: DefaultWindow, Slots: slots, CostClass: CostPaid, Effort: effortFor(w)}
	e, ok := a.cat.Default(w)
	if !ok {
		return p, nil
	}
	p.Ctx, _ = windowFor(a.cat, e.ID)
	req, err := a.request(ctx, http.MethodGet, "/v1/models/"+url.PathEscape(e.ID), nil)
	if err != nil {
		return p, err
	}
	resp, err := a.hc.Do(ctx, a.ep, req)
	var se *core.StatusError
	switch {
	case errors.As(err, &se) && se.Status == 404:
		return p, nil
	case err != nil:
		return CapacityProfile{}, classifyProbeErr(err)
	}
	var info modelInfo
	if json.Unmarshal(resp.Body, &info) == nil && info.MaxInputTokens > 0 {
		p.Ctx = info.MaxInputTokens
	}
	return p, nil
}

// price returns the metered cost of one turn for a model, from the catalog prices.
func (a *Anthropic) price(model string) func(Usage) float64 {
	e, ok := a.cat.Lookup(model)
	if !ok {
		return nil
	}
	return func(u Usage) float64 {
		in := float64(u.TokensIn) + 0.1*float64(u.CacheRead) + 1.25*float64(u.CacheWrite)
		return (in*e.PriceIn + float64(u.TokensOut)*e.PriceOut) / 1e6
	}
}

// Start runs the tool loop over the Messages interface.
func (a *Anthropic) Start(ctx context.Context, s Session) (Stream, error) {
	blocks, err := a.firstBlocks(s)
	if err != nil {
		return nil, err
	}
	w, err := a.newWire(s)
	if err != nil {
		return nil, err
	}
	return startLoop(ctx, loopConfig{Session: s, Wire: w, Link: AnthropicAPI, Now: a.opts.Now, Price: a.price(s.Model),
		Messages: []message{{Role: "user", Content: blocks}}})
}

// Resume loads the saved messages and continues them with the new input.
func (a *Anthropic) Resume(ctx context.Context, s Session, in Input) (Stream, error) {
	w, err := a.newWire(s)
	if err != nil {
		return nil, err
	}
	return resumeLoop(ctx, loopConfig{Session: s, Wire: w, Link: AnthropicAPI, Now: a.opts.Now, Price: a.price(s.Model)}, in.Text)
}

// layerNames are the brief parts in send order.
var layerNames = [5]string{"tools", "role", "repo", "standards", "brief"}

// firstBlocks builds the first user message: one block per brief part, with the planned markers, and records the plan.
func (a *Anthropic) firstBlocks(s Session) ([]block, error) {
	parts := s.Parts
	empty := true
	for _, p := range parts {
		empty = empty && p == ""
	}
	if empty {
		b, err := os.ReadFile(filepath.Join(s.Dir, "brief.md"))
		if err != nil {
			return nil, fmt.Errorf("read the brief: %w", err)
		}
		parts[4] = string(b)
	}
	var layers []cache.Layer
	var blocks []block
	for i, p := range parts {
		if p == "" {
			continue
		}
		sum := sha256.Sum256([]byte(p))
		layers = append(layers, cache.Layer{Name: layerNames[i], Hash: hex.EncodeToString(sum[:])})
		blocks = append(blocks, block{Type: "text", Text: p})
	}
	plan := cache.PlanMarkers(layers)
	for _, name := range plan.Fixed {
		for i, l := range layers {
			if l.Name != name {
				continue
			}
			blocks[i].CacheControl = &cacheControl{Type: "ephemeral"}
			if name == "repo" {
				blocks[i].CacheControl.TTL = "1h"
			}
		}
	}
	if s.Cache != nil {
		raw, _ := json.Marshal(plan)
		key := s.Cache.Key("plan", string(AnthropicAPI), s.Agent, s.Model)
		if err := s.Cache.Put(cache.NSPrompt, key, raw); err != nil {
			return nil, fmt.Errorf("store the marker plan: %w", err)
		}
	}
	return blocks, nil
}
