package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
)

const apiKey = "sk-ant-planted-key-123"

// fakeAPI is a recorded Messages interface: it replays scripted replies and keeps every request.
type fakeAPI struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []map[string]json.RawMessage
	raw     [][]byte
	headers []http.Header
	replies []func(w http.ResponseWriter, n int)
	models  string
}

// newFakeAPI starts the server; replies are consumed one per /v1/messages call.
func newFakeAPI(t *testing.T, replies ...func(w http.ResponseWriter, n int)) *fakeAPI {
	f := &fakeAPI{
		t:       t,
		replies: replies,
		models: `{"data":[{"id":"claude-opus-5-5","max_input_tokens":1000000},{"id":"claude-sonnet-5",` +
			`"max_input_tokens":900000},{"id":"claude-haiku-5-5","max_input_tokens":800000}]}`,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/models":
		_, _ = io.WriteString(w, f.models)
	case strings.HasPrefix(r.URL.Path, "/v1/models/"):
		_, _ = io.WriteString(w, `{"id":"x","max_input_tokens":777000}`)
	case r.URL.Path == "/v1/messages":
		b, _ := io.ReadAll(r.Body)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.bodies, f.raw, f.headers = append(f.bodies, m), append(f.raw, b), append(f.headers, r.Header.Clone())
		n := len(f.bodies) - 1
		var reply func(http.ResponseWriter, int)
		if n < len(f.replies) {
			reply = f.replies[n]
		}
		f.mu.Unlock()
		if reply == nil {
			http.Error(w, `{"error":{"type":"invalid_request_error","message":"script exhausted"}}`, 400)
			return
		}
		reply(w, n)
	default:
		http.NotFound(w, r)
	}
}

// adapter returns the paid adapter over the fake server.
func (f *fakeAPI) adapter() *Anthropic {
	a, err := NewAnthropic(AnthropicOptions{BaseURL: f.srv.URL, Key: apiKey})
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

// session returns an API-link session with a five-part brief.
func (f *fakeAPI) session(host ToolHost, gate Gate) Session {
	dir := f.t.TempDir()
	return Session{ID: "sid", Dir: dir, Agent: "builder", Link: AnthropicAPI, Model: "claude-sonnet-5-5",
		Profile: CapacityProfile{Ctx: 1000000, Effort: "medium"},
		Schema:  json.RawMessage(resultSchema),
		Tools:   host,
		Gate:    gate,
		Parts:   [5]string{"tool text", "role text", "repo guidance", "standards", "the task"}}
}

// memCache is an in-memory cache handle.
type memCache struct {
	mu   sync.Mutex
	puts map[string][]byte
}

func (m *memCache) Key(in ...string) cache.Key { return cache.Key(strings.Join(in, "|")) }

func (m *memCache) Get(ns cache.Namespace, k cache.Key) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.puts[string(ns)+"/"+string(k)]
	return b, ok
}

func (m *memCache) Put(ns cache.Namespace, k cache.Key, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.puts == nil {
		m.puts = map[string][]byte{}
	}
	m.puts[string(ns)+"/"+string(k)] = d
	return nil
}

func TestAnthropicSessionEndToEnd(t *testing.T) {
	f := newFakeAPI(t, toolReply("tu1", "read"), textReply("all done"), textReply(`{"done":true}`))
	host := &fakeHost{}
	a := f.adapter()
	st, err := a.Start(context.Background(), f.session(host, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v", err)
	}
	r := lastResult(t, evs)
	if string(r.Output) != `{"done":true}` {
		t.Errorf("output = %s", r.Output)
	}
	if len(host.calls) != 1 || host.calls[0] != `read {"path":"a.go"}` {
		t.Errorf("tool calls = %v", host.calls)
	}
	if r.Usage.TokensIn != 90 || r.Usage.CacheRead != 9 || r.Usage.CacheWrite != 12 || r.Usage.USD <= 0 ||
		r.Usage.USDEquiv != 0 {
		t.Errorf("usage = %+v", r.Usage)
	}
	var hr *Headroom
	for _, e := range evs {
		if e.Kind == KindHeadroom {
			hr = e.Headroom
		}
	}
	if hr == nil || hr.Name != "requests" || hr.PercentLeft() != 40 {
		t.Errorf("headroom event = %+v", hr)
	}

	h := f.headers[0]
	if h.Get("X-Api-Key") != apiKey || h.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("headers = %v", h)
	}
	b0 := f.bodies[0]
	var model string
	_ = json.Unmarshal(b0["model"], &model)
	if model != "claude-sonnet-5-5" || string(b0["stream"]) != "true" ||
		string(b0["cache_control"]) != `{"type":"ephemeral"}` {
		t.Errorf("body model %q stream %s cache_control %s", model, b0["stream"], b0["cache_control"])
	}
	if !strings.Contains(string(b0["output_config"]), `"effort":"medium"`) ||
		strings.Contains(string(b0["output_config"]), "format") {
		t.Errorf("work-turn output_config = %s", b0["output_config"])
	}
	for _, k := range []string{"tool_choice", "thinking"} {
		if _, ok := b0[k]; ok {
			t.Errorf("request sends %s", k)
		}
	}
	var tools []ToolDef
	_ = json.Unmarshal(b0["tools"], &tools)
	if len(tools) != 2 || tools[0].Name != "read" || tools[1].Name != "run" {
		t.Errorf("tools = %+v", tools)
	}
	var msgs []message
	_ = json.Unmarshal(b0["messages"], &msgs)
	blocks := msgs[0].Content
	if len(blocks) != 5 || blocks[2].CacheControl == nil || blocks[2].CacheControl.TTL != "1h" ||
		blocks[4].CacheControl == nil || blocks[4].CacheControl.TTL != "" {
		t.Errorf("markers = %+v", blocks)
	}
	if n := strings.Count(string(f.raw[0]), `"cache_control"`); n != 3 {
		t.Errorf("request has %d cache_control fields, want 2 explicit plus the automatic one", n)
	}
	if !strings.Contains(string(f.bodies[2]["output_config"]), `"format":{"type":"json_schema","schema":`) {
		t.Errorf("final call output_config = %s", f.bodies[2]["output_config"])
	}
}

func TestAnthropicStoresMarkerPlanAndKeepsPrefixStable(t *testing.T) {
	f := newFakeAPI(t)
	a := f.adapter()
	mc := &memCache{}
	s1 := f.session(nil, nil)
	s1.Cache = mc
	b1, err := a.firstBlocks(s1)
	if err != nil {
		t.Fatal(err)
	}
	s2 := f.session(nil, nil)
	s2.Parts[4] = "a different task"
	b2, _ := a.firstBlocks(s2)
	p1, _ := json.Marshal(b1[:4])
	p2, _ := json.Marshal(b2[:4])
	if string(p1) != string(p2) {
		t.Error("two sessions of one agent must share every prefix byte before the task")
	}
	if len(mc.puts) != 1 {
		t.Fatalf("plans stored = %d", len(mc.puts))
	}
	for k, raw := range mc.puts {
		var plan cache.MarkerPlan
		_ = json.Unmarshal(raw, &plan)
		if !strings.HasPrefix(k, "prompt/") || strings.Join(plan.Fixed, ",") != "repo,brief" || !plan.Auto ||
			len(plan.Layers) != 5 {
			t.Errorf("plan %s = %+v", k, plan)
		}
	}
}

func TestAnthropicRefusesFourExplicitMarkers(t *testing.T) {
	w := &anthropicWire{model: "claude-sonnet-5-5", max: 10}
	mk := func(n int) []message {
		var bs []block
		for i := 0; i < n; i++ {
			bs = append(bs, block{Type: "text", Text: "x", CacheControl: &cacheControl{Type: "ephemeral"}})
		}
		return []message{{Role: "user", Content: bs}}
	}
	if _, err := w.buildRequest(turnRequest{Messages: mk(3)}); err != nil {
		t.Errorf("3 explicit markers fit beside the automatic one: %v", err)
	}
	if _, err := w.buildRequest(turnRequest{Messages: mk(4)}); err == nil {
		t.Error("4 explicit markers must fail before sending")
	}
}

func TestAnthropicStartErrors(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		reply func(http.ResponseWriter, int)
		check func(t *testing.T, err error)
	}{
		{"404", failWith(404, `{"error":{"type":"not_found_error","message":"model"}}`), func(t *testing.T, err error) {
			if !errors.Is(err, ErrModelGone) {
				t.Errorf("err = %v", err)
			}
		}},
		{"401", failWith(401, `{"error":{"type":"authentication_error","message":"no"}}`), func(t *testing.T, err error) {
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("err = %v", err)
			}
		}},
		{"spend 429", failWith(
			429,
			`{"error":{"type":"x","code":"enforced_spend_limit_reached"}}`,
		), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapSpend || pl.Link != AnthropicAPI {
				t.Errorf("err = %v", err)
			}
		}},
		{"spend 400", failWith(
			400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"You have reached your specified API usage `+
				`limits. You will regain access on 2030-01-01"}}`,
		), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapSpend {
				t.Errorf("err = %v", err)
			}
		}},
		{"other 400 stays final", failWith(
			400,
			`{"error":{"type":"invalid_request_error","message":"bad"}}`,
		), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if err == nil || errors.As(err, &pl) {
				t.Errorf("err = %v", err)
			}
		}},
		{"retry-after over a minute", failWith(
			429,
			`{"error":{"type":"rate_limit_error"}}`,
			"Retry-After",
			"3600",
		), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapWindow || pl.ResetAt.Before(now.Add(55*time.Minute)) {
				t.Errorf("err = %v limit %+v", err, pl)
			}
		}},
		{"quota code", failWith(429, `{"error":{"type":"quota_exceeded"}}`), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapQuota {
				t.Errorf("err = %v", err)
			}
		}},
	}
	for _, c := range cases {
		f := newFakeAPI(t, c.reply)
		_, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}, nil))
		c.check(t, err)
		if n := len(f.bodies); n != 1 {
			t.Errorf("%s: %d attempts, want 1", c.name, n)
		}
	}
}

func TestAnthropicKeyNeverInErrors(t *testing.T) {
	f := newFakeAPI(t, failWith(400, `{"error":{"type":"invalid_request_error","message":"rejected key `+apiKey+`"}}`))
	_, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}, nil))
	if err == nil || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error text must not hold the key: %v", err)
	}
}

func TestAnthropicStreamDropRetriesFromStart(t *testing.T) {
	drop := func(w http.ResponseWriter, _ int) {
		sse(w, start(
			30,
		),
			[2]string{
				"content_block_start",
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			},
			[2]string{
				"content_block_delta",
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"PARTIAL-"}}`,
			})
	}
	f := newFakeAPI(t, drop, textReply("clean"), textReply(`{"done":true}`))
	st, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	if len(f.bodies) != 3 {
		t.Errorf("requests = %d, want 3 (one dropped, one retry, one final)", len(f.bodies))
	}
	if strings.Contains(string(f.raw[2]), "PARTIAL") {
		t.Error("the dropped partial text reached the history")
	}
}

func TestAnthropicResume(t *testing.T) {
	f := newFakeAPI(t, textReply("done"), textReply(`{"done":true}`), textReply("more"), textReply(`{"done":true}`))
	a := f.adapter()
	s := f.session(&fakeHost{}, nil)
	st, err := a.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	_ = st.Close()
	st2, err := a.Resume(context.Background(), s, Input{Text: "one more thing"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	if _, err := drain(t, st2); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.raw[2]), "one more thing") || !strings.Contains(string(f.raw[2]), "the task") {
		t.Errorf("resumed request lacks the history or the new input: %s", f.raw[2])
	}
}

func TestAnthropicProbeAndProfile(t *testing.T) {
	f := newFakeAPI(t)
	a := f.adapter()
	res, err := a.Probe(context.Background())
	if err != nil || !res.OK {
		t.Fatalf("Probe = %+v %v", res, err)
	}
	if res.Models[Heavy] != "claude-opus-5-5" || res.Models[Standard] != "claude-sonnet-5" ||
		res.Models[Light] != "claude-haiku-5-5" {
		t.Errorf("models = %v (a missing latest falls back to the prior model)", res.Models)
	}
	if res.Ctx != 800000 || res.CtxSource != "models_api" {
		t.Errorf("ctx %d from %s", res.Ctx, res.CtxSource)
	}
	p, err := a.Profile(context.Background(), Heavy)
	if err != nil || p.Ctx != 777000 || p.CostClass != CostPaid || p.Effort != "high" || p.Slots != 3 {
		t.Errorf("profile = %+v %v", p, err)
	}

	noKey, _ := NewAnthropic(AnthropicOptions{BaseURL: f.srv.URL})
	if _, err := noKey.Probe(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("no key = %v", err)
	}
	f.models = `{"data":[]}`
	if r, err := a.Probe(context.Background()); !errors.Is(err, ErrUnavailable) || r.OK {
		t.Errorf("no usable model = %v", err)
	}
	f.srv.Close()
	if r, err := a.Probe(context.Background()); !errors.Is(err, ErrUnavailable) || r.OK {
		t.Errorf("server down = %+v %v", r, err)
	}
}

func TestAnthropicProbeRefusedKey(t *testing.T) {
	srv := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { http.Error(w, `{"error":{}}`, http.StatusUnauthorized) },
		),
	)
	defer srv.Close()
	a, _ := NewAnthropic(AnthropicOptions{BaseURL: srv.URL, Key: apiKey})
	if _, err := a.Probe(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401 = %v", err)
	}
}

// textReply is a streamed text-only reply.
func textReply(text string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("Anthropic-Ratelimit-Requests-Limit", "100")
		w.Header().Set("Anthropic-Ratelimit-Requests-Remaining", "40")
		w.Header().Set("Anthropic-Ratelimit-Requests-Reset", "2030-01-01T00:00:00Z")
		half := len(text) / 2
		sse(w, start(30),
			[2]string{
				"content_block_start",
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			},
			[2]string{
				"content_block_delta",
				mustJSON(map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": text[:half]},
				}),
			},
			[2]string{
				"content_block_delta",
				mustJSON(map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": text[half:]},
				}),
			},
			[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			[2]string{
				"message_delta",
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`,
			},
			stop())
	}
}

// sse writes server-sent events.
func sse(w http.ResponseWriter, events ...[2]string) {
	w.Header().Set("content-type", "text/event-stream")
	for _, e := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
	}
}

func start(in int) [2]string {
	return [2]string{
		"message_start",
		fmt.Sprintf(
			`{"type":"message_start","message":{"usage":{"input_tokens":%d,"output_tokens":1,"cache_read_input_tokens":3,`+
				`"cache_creation_input_tokens":4}}}`,
			in,
		),
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func stop() [2]string { return [2]string{"message_stop", `{"type":"message_stop"}`} }

// toolReply is a streamed reply that calls one tool with split arguments.
func toolReply(id, name string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		sse(w, start(30),
			[2]string{
				"content_block_start",
				mustJSON(map[string]any{
					"type":          "content_block_start",
					"index":         0,
					"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
				}),
			},
			[2]string{
				"content_block_delta",
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			},
			[2]string{
				"content_block_delta",
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
			},
			[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			[2]string{
				"message_delta",
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			},
			stop())
	}
}

// failWith replies with a status and JSON body.
func failWith(status int, body string, hdr ...string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}
