package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// LocalEndpoint is the optional local link's server.
type LocalEndpoint struct {
	URL        string            // the server's /v1 base, such as http://localhost:11434/v1
	Models     map[Weight]string // a model name per weight
	Ctx        int               // context tokens; required for a server that is not Ollama
	Key        string            // env:NAME reference to an API key the server needs; optional
	SelfHosted bool              // marks a server that is not on loopback as free
	// seconds to wait for the first byte; the default is 300 on loopback
	FirstByteS int
}

// LocalOptions tunes the local adapter.
type LocalOptions struct {
	Endpoint LocalEndpoint
	HTTP     *core.Client        // shared HTTP client; nil builds one
	Getenv   func(string) string // reads the key named by Endpoint.Key; nil uses os.Getenv
	// how long Ollama keeps the model loaded, such as "90m"; empty means 60m
	KeepAlive string
	Slots     int              // concurrent sessions on the link; 0 means 1
	Catalog   *Catalog         // model catalog; nil loads the embedded one
	Now       func() time.Time // clock; nil uses time.Now
	Faults    Recoverer        // guards the loop goroutine; nil prints a panic on standard error
}

// LocalProvider is the optional local adapter: an OpenAI-compatible server on this machine.
type LocalProvider struct {
	opts LocalOptions
	cat  *Catalog
	hc   *core.Client
	ep   core.Endpoint
	base string // the /v1 base without a trailing slash
	root string // scheme and host, for Ollama's own paths
	key  string
}

// reachWait is how long the local server has to answer the reachability request.
const reachWait = time.Second

// NewLocal builds the local adapter.
func NewLocal(opts LocalOptions) (*LocalProvider, error) {
	base := strings.TrimRight(opts.Endpoint.URL, "/")

	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("local endpoint URL %q is not a URL", opts.Endpoint.URL)
	}

	p := &LocalProvider{
		opts: opts,
		cat:  opts.Catalog,
		hc:   opts.HTTP,
		base: base,
		root: u.Scheme + "://" + u.Host,
	}
	if p.cat == nil {
		p.cat, _ = LoadCatalog()
	}

	if ref := opts.Endpoint.Key; ref != "" {
		get := opts.Getenv
		if get == nil {
			get = os.Getenv
		}

		p.key = get(strings.TrimPrefix(ref, "env:"))
	}

	if p.hc == nil {
		var secrets []string
		if p.key != "" {
			secrets = append(secrets, p.key)
		}

		if p.hc, err = core.NewClient(core.Options{Secrets: secrets}); err != nil {
			return nil, err
		}
	} else if p.key != "" {
		p.hc.AddSecrets(p.key)
	}

	p.ep = core.Endpoint{
		Hosts:      []string{u.Host},
		Methods:    []string{http.MethodGet, http.MethodPost},
		SelfHosted: opts.Endpoint.SelfHosted,
		FirstByte:  time.Duration(opts.Endpoint.FirstByteS) * time.Second,
	}

	return p, nil
}

// CostClass is free on loopback or when the server is marked self-hosted, else paid.
func (e LocalEndpoint) CostClass() string {
	u, err := url.Parse(e.URL)
	if err == nil && (e.SelfHosted || isLoopbackHost(u.Hostname())) {
		return CostFree
	}

	return CostPaid
}

// Link returns Local.
func (p *LocalProvider) Link() Link { return Local }

// Probe checks the server answers, whether it is Ollama, and the context of each weight's model.
func (p *LocalProvider) Probe(ctx context.Context) (ProbeResult, error) {
	res := ProbeResult{Link: Local, Models: map[Weight]string{}}

	fail := func(err error) (ProbeResult, error) {
		res.Reason = err.Error()

		return res, err
	}
	if err := p.reachable(ctx); err != nil {
		return fail(err)
	}

	res.Ollama = p.isOllama(ctx)
	seen := map[string]bool{}

	for _, w := range weightOrder {
		m := p.modelFor(w)
		if m == "" {
			continue
		}

		res.Models[w] = m
		if seen[m] {
			continue
		}

		seen[m] = true

		n, src, err := p.measure(ctx, m, res.Ollama)
		if err != nil {
			return fail(err)
		}

		if res.Ctx == 0 || n < res.Ctx {
			res.Ctx, res.CtxSource = n, src
		}
	}

	if len(res.Models) == 0 {
		return fail(fmt.Errorf("the local link lists no model: %w", ErrUnavailable))
	}

	res.OK = true

	return res, nil
}

// Profile measures the capacity profile of one weight's model.
func (p *LocalProvider) Profile(ctx context.Context, w Weight) (CapacityProfile, error) {
	// localSlots is the default number of concurrent sessions on the local link.
	const localSlots = 1

	if err := p.reachable(ctx); err != nil {
		return CapacityProfile{}, err
	}

	m := p.modelFor(w)
	if m == "" {
		return CapacityProfile{}, fmt.Errorf("the local link lists no model: %w", ErrUnavailable)
	}

	n, _, err := p.measure(ctx, m, p.isOllama(ctx))
	if err != nil {
		return CapacityProfile{}, err
	}

	slots := p.opts.Slots
	if slots <= 0 {
		slots = localSlots
	}

	return CapacityProfile{
		Ctx:       n,
		Slots:     slots,
		CostClass: p.opts.Endpoint.CostClass(),
		Effort:    effortFor(w),
	}, nil
}

// Start runs the tool loop over the chat interface.
func (p *LocalProvider) Start(ctx context.Context, s Session) (Stream, error) {
	blocks, err := p.plainBlocks(s)
	if err != nil {
		return nil, err
	}

	return startLoop(ctx, loopConfig{
		Session:  s,
		Wire:     p.newWire(s),
		Link:     Local,
		Now:      p.opts.Now,
		Faults:   p.opts.Faults,
		Messages: []message{{Role: "user", Content: blocks}},
	})
}

// Resume loads the saved messages and continues them with the new input.
func (p *LocalProvider) Resume(ctx context.Context, s Session, in Input) (Stream, error) {
	return resumeLoop(ctx, loopConfig{
		Session: s,
		Wire:    p.newWire(s),
		Link:    Local,
		Now:     p.opts.Now,
		Faults:  p.opts.Faults,
	}, in.Text)
}

// request builds one request, with the key when the server needs one.
func (p *LocalProvider) request(
	ctx context.Context,
	method, target string,
	body []byte,
) (*http.Request, error) {
	var (
		req *http.Request
		err error
	)
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, target, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("content-type", "application/json")

	if p.key != "" {
		req.Header.Set("authorization", "Bearer "+p.key)
	}

	return req, nil
}

// get fetches one path under the server and returns the body.
func (p *LocalProvider) get(ctx context.Context, target string) ([]byte, error) {
	req, err := p.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.hc.Do(ctx, p.ep, req)
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

// modelFor returns the model a weight uses: the endpoint's own, else the nearest listed weight's,
// else the catalog's.
func (p *LocalProvider) modelFor(w Weight) string {
	if m := p.opts.Endpoint.Models[w]; m != "" {
		return m
	}

	at := indexOfWeight(w)
	for d := 1; d < len(weightOrder); d++ {
		for _, i := range []int{at - d, at + d} {
			if i >= 0 && i < len(weightOrder) {
				if m := p.opts.Endpoint.Models[weightOrder[i]]; m != "" {
					return m
				}
			}
		}
	}

	if e, ok := p.cat.LocalModel(w); ok {
		return e.ID
	}

	return ""
}

// reachable checks that the server answers GET /models within a second.
func (p *LocalProvider) reachable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, reachWait)
	defer cancel()

	if _, err := p.get(ctx, p.base+"/models"); err != nil {
		var se *core.StatusError
		if errors.As(err, &se) && (se.Status == 401 || se.Status == 403) {
			return fmt.Errorf(
				"the local server refused the key (HTTP %d): %w",
				se.Status,
				ErrUnauthorized,
			)
		}

		return fmt.Errorf("local server %s: %v: %w", p.root, err, ErrUnavailable)
	}

	return nil
}

// isOllama reports whether GET /api/version answers on the server's host.
func (p *LocalProvider) isOllama(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, reachWait)
	defer cancel()

	b, err := p.get(ctx, p.root+"/api/version")

	return err == nil && json.Valid(b)
}

// loadedContext returns the context length Ollama reports for a loaded model, or 0 when it is not
// loaded.
func (p *LocalProvider) loadedContext(ctx context.Context, model string) (int, error) {
	b, err := p.get(ctx, p.root+"/api/ps")
	if err != nil {
		return 0, fmt.Errorf("read /api/ps: %v: %w", err, ErrUnavailable)
	}

	var ps struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Context int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &ps); err != nil {
		return 0, fmt.Errorf("parse /api/ps: %w", ErrUnavailable)
	}

	for _, m := range ps.Models {
		if m.Name == model || m.Model == model || strings.TrimSuffix(m.Name, ":latest") == model {
			return m.Context, nil
		}
	}

	return 0, nil
}

// load asks Ollama to load a model with an empty request and keep it loaded.
func (p *LocalProvider) load(ctx context.Context, model string) error {
	keep := p.opts.KeepAlive
	if keep == "" {
		keep = "60m"
	}

	body, _ := json.Marshal(map[string]any{"model": model, "keep_alive": keep})

	req, err := p.request(ctx, http.MethodPost, p.root+"/api/generate", body)
	if err != nil {
		return err
	}

	if _, err := p.hc.Do(ctx, p.ep, req); err != nil {
		return fmt.Errorf("load %s: %v: %w", model, err, ErrUnavailable)
	}

	return nil
}

// measure returns the context a model runs with and where the number came from.
func (p *LocalProvider) measure(
	ctx context.Context,
	model string,
	ollama bool,
) (int, string, error) {
	typed := p.opts.Endpoint.Ctx
	if !ollama {
		if typed <= 0 {
			return 0, "", fmt.Errorf(
				"a server that is not Ollama needs its context size set: %w",
				ErrUnavailable,
			)
		}

		return typed, "endpoint", nil
	}

	n, err := p.loadedContext(ctx, model)
	if err == nil && n == 0 {
		if err = p.load(ctx, model); err == nil {
			n, err = p.loadedContext(ctx, model)
		}
	}

	if err != nil {
		return 0, "", err
	}

	if n <= 0 {
		return 0, "", fmt.Errorf("ollama reports no context for %s: %w", model, ErrUnavailable)
	}

	if typed > 0 && typed < n {
		n = typed
	}

	return n, "ollama_ps", nil
}

// plainBlocks returns the brief as one text block, with no cache markers.
func (p *LocalProvider) plainBlocks(s Session) ([]block, error) {
	var parts []string

	for _, x := range s.Parts {
		if x != "" {
			parts = append(parts, x)
		}
	}

	if len(parts) == 0 {
		b, err := os.ReadFile(filepath.Join(s.Dir, "brief.md"))
		if err != nil {
			return nil, fmt.Errorf("read the brief: %w", err)
		}

		parts = []string{string(b)}
	}

	return []block{{Type: "text", Text: strings.Join(parts, "\n\n")}}, nil
}

// isLoopbackHost reports whether a host name or address is this machine.
func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}

	ip := net.ParseIP(h)

	return ip != nil && ip.IsLoopback()
}

// indexOfWeight returns the position of a weight from heaviest to lightest, or -1.
func indexOfWeight(w Weight) int {
	for i, x := range weightOrder {
		if x == w {
			return i
		}
	}

	return -1
}
