package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// OpenAIAPIOptions tunes the paid OpenAI adapter; Key and Models are the fields a working link needs.
type OpenAIAPIOptions struct {
	BaseURL string                 // the /v1 base; empty means https://api.openai.com/v1
	Key     string                 // the API key's value, resolved by the caller from its env: reference
	Models  map[Weight]OpenAIModel // the model per weight; a weight with none takes the nearest weight's
	HTTP    *core.Client           // shared HTTP client; nil builds one that redacts the key
	Slots   int                    // concurrent sessions allowed on the link; 0 means 3
	Now     func() time.Time       // clock; nil uses time.Now
	Faults  Recoverer              // guards the loop goroutine; nil prints a panic on standard error
}

// OpenAIChat is the paid HTTP adapter for OpenAI's chat completions interface.
type OpenAIChat struct {
	opts OpenAIAPIOptions
	hc   *core.Client
	ep   core.Endpoint
	base string
}

// NewOpenAIAPI builds the paid OpenAI adapter and its HTTP client.
func NewOpenAIAPI(opts OpenAIAPIOptions) (*OpenAIChat, error) {
	const openAIBase = "https://api.openai.com/v1"

	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = openAIBase
	}

	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("openai base URL %q is not a URL", opts.BaseURL)
	}

	a := &OpenAIChat{opts: opts, hc: opts.HTTP, base: base}
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

// Link returns OpenAIAPI.
func (a *OpenAIChat) Link() Link { return OpenAIAPI }

// Probe lists the key's models and keeps each weight's configured model that the list holds.
func (a *OpenAIChat) Probe(ctx context.Context) (ProbeResult, error) {
	res := ProbeResult{Link: OpenAIAPI, Models: map[Weight]string{}}
	if a.opts.Key == "" {
		res.Reason = "no API key is set"
		return res, fmt.Errorf("%s: %w", res.Reason, ErrUnauthorized)
	}

	list, err := a.listModels(ctx)
	if err != nil {
		res.Reason = err.Error()
		return res, err
	}

	have := map[string]bool{}
	for _, id := range list {
		have[id] = true
	}

	for _, w := range weightOrder {
		m, ok := openAIModelFor(a.opts.Models, w)
		if !ok || !have[m.ID] {
			continue
		}

		res.Models[w] = m.ID
		if win := openAIWindow(m); res.Ctx == 0 || win < res.Ctx {
			res.Ctx = win
		}
	}

	if len(res.Models) == 0 {
		res.Reason = "the key can use none of the configured models"
		return res, fmt.Errorf("%s: %w", res.Reason, ErrUnavailable)
	}

	res.CtxSource, res.OK = "endpoint", true
	return res, nil
}

// Profile checks the weight's model is one the key can use and sizes the session from its configured window.
func (a *OpenAIChat) Profile(ctx context.Context, w Weight) (CapacityProfile, error) {
	const openAISlots = 3

	if a.opts.Key == "" {
		return CapacityProfile{}, fmt.Errorf("no API key is set: %w", ErrUnauthorized)
	}

	m, ok := openAIModelFor(a.opts.Models, w)
	if !ok {
		return CapacityProfile{}, fmt.Errorf("no OpenAI model is configured: %w", ErrUnavailable)
	}

	req, err := a.request(ctx, http.MethodGet, "/models/"+url.PathEscape(m.ID), nil)
	if err != nil {
		return CapacityProfile{}, err
	}

	if _, err := a.hc.Do(ctx, a.ep, req); err != nil {
		var se *core.StatusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			return CapacityProfile{}, fmt.Errorf("model %q is not listed for the key: %w", m.ID, ErrUnavailable)
		}
		return CapacityProfile{}, classifyProbeErr(err)
	}

	slots := a.opts.Slots
	if slots <= 0 {
		slots = openAISlots
	}

	return CapacityProfile{Ctx: openAIWindow(m), Slots: slots, CostClass: CostPaid, Effort: effortFor(w)}, nil
}

// Start runs the tool loop over the chat completions interface.
func (a *OpenAIChat) Start(ctx context.Context, s Session) (Stream, error) {
	m, price, err := a.price(s.Model)
	if err != nil {
		return nil, err
	}

	blocks, err := plainBlocks(s)
	if err != nil {
		return nil, err
	}

	return startLoop(ctx, loopConfig{
		Session:  s,
		Wire:     a.newWire(s, m),
		Link:     OpenAIAPI,
		Now:      a.opts.Now,
		Faults:   a.opts.Faults,
		Price:    price,
		Messages: []message{{Role: "user", Content: blocks}},
	})
}

// Resume loads the saved messages and continues them with the new input.
func (a *OpenAIChat) Resume(ctx context.Context, s Session, in Input) (Stream, error) {
	m, price, err := a.price(s.Model)
	if err != nil {
		return nil, err
	}

	return resumeLoop(ctx, loopConfig{
		Session: s,
		Wire:    a.newWire(s, m),
		Link:    OpenAIAPI,
		Now:     a.opts.Now,
		Faults:  a.opts.Faults,
		Price:   price,
	}, in.Text)
}

// listModels returns the identifiers of the models the key can use.
func (a *OpenAIChat) listModels(ctx context.Context) ([]string, error) {
	req, err := a.request(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.hc.Do(ctx, a.ep, req)
	if err != nil {
		return nil, classifyProbeErr(err)
	}

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("parse the model list: %w", ErrUnavailable)
	}

	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}

	return ids, nil
}

// request builds one authenticated request under the base.
func (a *OpenAIChat) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	return a.send(ctx, method, a.base+path, body)
}

// send builds one authenticated request to a full address.
func (a *OpenAIChat) send(ctx context.Context, method, target string, body []byte) (*http.Request, error) {
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}

	var (
		req *http.Request
		err error
	)
	if rd != nil {
		req, err = http.NewRequestWithContext(ctx, method, target, rd)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, target, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("authorization", "Bearer "+a.opts.Key)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// price finds the configured model and returns it with its metered cost of one turn.
// Cache reads count at the full input price, so a paid session never under-reports spend.
// A model with no configured price fails closed with ErrUnpriced.
func (a *OpenAIChat) price(model string) (OpenAIModel, func(u Usage, write1h int) float64, error) {
	for _, m := range a.opts.Models {
		if m.ID != model {
			continue
		}
		if m.PriceIn <= 0 && m.PriceOut <= 0 {
			break
		}

		return m, func(u Usage, _ int) float64 {
			in := float64(u.TokensIn + u.CacheRead)
			return (in*m.PriceIn + float64(u.TokensOut)*m.PriceOut) / 1e6
		}, nil
	}

	return OpenAIModel{}, nil, fmt.Errorf("%q: %w", model, ErrUnpriced)
}

// newWire gathers what every request of one session repeats.
func (a *OpenAIChat) newWire(s Session, m OpenAIModel) *chatWire {
	tune := chatTuning{systemRole: "developer", maxTokens: m.MaxOut}
	if m.Reasoning {
		tune.effort = s.Profile.Effort
	}

	send := chatSender{
		hc:      a.hc,
		ep:      a.ep,
		base:    a.base,
		request: a.send,
		fail:    normalizeOpenAIErr,
		rates:   a.rates,
	}

	return newChatWire(send, tune, s)
}

// rates reads the x-ratelimit headers of a response into a requests bucket and a tokens bucket.
func (a *OpenAIChat) rates(h http.Header) []Headroom {
	now := time.Now
	if a.opts.Now != nil {
		now = a.opts.Now
	}

	var out []Headroom

	for _, name := range []string{"requests", "tokens"} {
		rem, err := strconv.ParseInt(h.Get("x-ratelimit-remaining-"+name), 10, 64)
		if err != nil {
			continue
		}

		hr := Headroom{Name: name, Remaining: rem}
		hr.Limit, _ = strconv.ParseInt(h.Get("x-ratelimit-limit-"+name), 10, 64)

		if d, err := time.ParseDuration(h.Get("x-ratelimit-reset-" + name)); err == nil {
			hr.Reset = now().Add(d)
		}

		out = append(out, hr)
	}

	return out
}

// openAISpendCodes are the 429 error codes that report credits or a spend limit used up; retrying cannot clear them.
var openAISpendCodes = []string{
	"credit_balance_exhausted",
	"organization_spend_limit_exceeded",
	"project_spend_limit_exceeded",
	"organization_usage_limit_exceeded",
}

// normalizeOpenAIErr turns a 429 that reports spent credits or a spend limit into the limit error the loop maps to a cap.
func normalizeOpenAIErr(err error) error {
	var se *core.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		return err
	}

	var v struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(se.Body, &v) != nil {
		return err
	}

	for _, c := range openAISpendCodes {
		if v.Error.Code == c || v.Error.Type == c {
			return &core.LimitError{Kind: core.LimitSpend, Status: se.Status, Code: c}
		}
	}

	return err
}
