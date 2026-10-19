package llm

// The fake server follows OpenAI's published chat completions behaviour; no test calls the real service.
//   - streaming chunks, tool_calls deltas, finish_reason and the final usage chunk:
//     https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events
//   - 401 and 429 error bodies (type insufficient_quota, the credit and spend-limit codes):
//     https://developers.openai.com/api/docs/guides/error-codes
//   - x-ratelimit-* response headers and their duration values: https://developers.openai.com/api/docs/guides/rate-limits
//   - the models list, which carries no context window: https://developers.openai.com/api/reference/resources/models/methods/list

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

const openAIKey = "sk-proj-planted-key-456"

// fakeOpenAI is a recorded chat completions server: it replays scripted replies and keeps every request.
type fakeOpenAI struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []map[string]json.RawMessage
	headers []http.Header
	replies []func(w http.ResponseWriter, n int)
	listed  string
	modelRC int
}

// newFakeOpenAI starts the server; replies are consumed one per /v1/chat/completions call.
func newFakeOpenAI(t *testing.T, replies ...func(w http.ResponseWriter, n int)) *fakeOpenAI {
	f := &fakeOpenAI{
		t:       t,
		replies: replies,
		listed:  `{"object":"list","data":[{"id":"gpt-big","object":"model"},{"id":"gpt-small","object":"model"}]}`,
		modelRC: http.StatusOK,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/models":
		if f.modelRC != http.StatusOK {
			w.WriteHeader(f.modelRC)
			_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error",`+
				`"code":"invalid_api_key"}}`)
			return
		}
		_, _ = io.WriteString(w, f.listed)
	case strings.HasPrefix(r.URL.Path, "/v1/models/"):
		if strings.HasSuffix(r.URL.Path, "/gpt-gone") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"The model does not exist","type":"invalid_request_error",`+
				`"code":"model_not_found"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"x","object":"model","owned_by":"openai"}`)
	case r.URL.Path == "/v1/chat/completions":
		b, _ := io.ReadAll(r.Body)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.bodies, f.headers = append(f.bodies, m), append(f.headers, r.Header.Clone())
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

// models are the two models every test configures: a reasoning model for heavy and a plain one for the rest.
func openAIModels() map[Weight]OpenAIModel {
	return map[Weight]OpenAIModel{
		Heavy:    {ID: "gpt-big", Window: 400000, MaxOut: 32000, PriceIn: 2, PriceOut: 10, Reasoning: true},
		Standard: {ID: "gpt-small", Window: 128000, PriceIn: 1, PriceOut: 4},
	}
}

// adapter returns the paid adapter over the fake server, with a client that never sleeps between retries.
func (f *fakeOpenAI) adapter() *OpenAIChat {
	hc, err := core.NewClient(core.Options{Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		f.t.Fatal(err)
	}

	a, err := NewOpenAIAPI(OpenAIAPIOptions{BaseURL: f.srv.URL + "/v1", Key: openAIKey, Models: openAIModels(), HTTP: hc})
	if err != nil {
		f.t.Fatal(err)
	}

	return a
}

// session returns an interface-link session on the heavy model.
func (f *fakeOpenAI) session(host ToolHost) Session {
	return Session{ID: "sid", Dir: f.t.TempDir(), Agent: "builder", Link: OpenAIAPI, Model: "gpt-big",
		Profile: CapacityProfile{Ctx: 400000, Effort: "high"},
		Schema:  json.RawMessage(resultSchema),
		Tools:   host,
		Parts:   [5]string{"", "role text", "", "", "the task"}}
}

// openAIReply writes a scripted event stream body with optional response headers.
func openAIReply(body string, hdr ...string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("content-type", "text/event-stream")
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		_, _ = io.WriteString(w, body)
	}
}

// openAIText is a streamed text reply: a role chunk, the text, the finish chunk and the usage chunk with cached tokens.
func openAIText(text string) string {
	return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,` +
		`"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
		"data: " + mustJSON(map[string]any{
		"object": "chat.completion.chunk",
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil,
		}},
	}) + "\n\n" +
		`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,` +
		`"total_tokens":120,"prompt_tokens_details":{"cached_tokens":60}}}` + "\n\n" +
		"data: [DONE]\n\n"
}

func TestOpenAIAPISessionEndToEnd(t *testing.T) {
	f := newFakeOpenAI(t,
		openAIReply(chatToolCall("call_1", "read"), "x-ratelimit-limit-requests", "60",
			"x-ratelimit-remaining-requests", "24", "x-ratelimit-reset-requests", "1s",
			"x-ratelimit-limit-tokens", "150000", "x-ratelimit-remaining-tokens", "149000",
			"x-ratelimit-reset-tokens", "6m0s"),
		openAIReply(openAIText("all done")),
		openAIReply(openAIText(`{"done":true}`)),
	)
	host := &fakeHost{}
	s := f.session(host)
	s.Dir = t.TempDir()
	writeFile(t, s.Dir+"/system.md", "be exact")

	st, err := f.adapter().Start(context.Background(), s)
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

	// 3 turns: 10+100+100 prompt tokens with 60+60 cached; 3+20+20 completion tokens at 1x the input price.
	u := r.Usage
	if u.TokensIn != 10+40+40 || u.CacheRead != 120 || u.TokensOut != 43 || u.USDEquiv != 0 {
		t.Errorf("usage = %+v", u)
	}
	if want := (210.0*2 + 43.0*10) / 1e6; u.USD < want*0.999 || u.USD > want*1.001 {
		t.Errorf("USD = %v, want %v", u.USD, want)
	}

	var req, tok *Headroom
	for _, e := range evs {
		if e.Kind == KindHeadroom && e.Headroom.Name == "requests" {
			req = e.Headroom
		}
		if e.Kind == KindHeadroom && e.Headroom.Name == "tokens" {
			tok = e.Headroom
		}
	}
	if req == nil || req.PercentLeft() != 40 || req.Reset.IsZero() || tok == nil || tok.Remaining != 149000 {
		t.Errorf("headroom = %+v / %+v", req, tok)
	}

	h := f.headers[0]
	if h.Get("Authorization") != "Bearer "+openAIKey || h.Get("X-Api-Key") != "" {
		t.Errorf("headers = %v", h)
	}

	b0 := f.bodies[0]
	var model string
	_ = json.Unmarshal(b0["model"], &model)
	if model != "gpt-big" || string(b0["stream"]) != "true" || string(b0["max_completion_tokens"]) != "32000" ||
		string(b0["reasoning_effort"]) != `"high"` || string(b0["stream_options"]) != `{"include_usage":true}` {
		t.Errorf("body = %s %s %s %s", model, b0["max_completion_tokens"], b0["reasoning_effort"], b0["stream_options"])
	}
	if _, ok := b0["max_tokens"]; ok {
		t.Error("request sends the deprecated max_tokens")
	}

	var tools []chatTool
	_ = json.Unmarshal(b0["tools"], &tools)
	if len(tools) != 2 || tools[0].Type != "function" || tools[0].Function.Name != "read" {
		t.Errorf("tools = %+v", tools)
	}

	var msgs []chatMessage
	_ = json.Unmarshal(b0["messages"], &msgs)
	if len(msgs) != 2 || msgs[0].Role != "developer" || msgs[0].Content != "be exact" || msgs[1].Role != "user" {
		t.Errorf("first messages = %+v", msgs)
	}

	_ = json.Unmarshal(f.bodies[1]["messages"], &msgs)
	var sawCall, sawTool bool
	for _, m := range msgs {
		sawCall = sawCall || (m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call_1" &&
			m.ToolCalls[0].Function.Arguments == `{"path":"a.go"}`)
		sawTool = sawTool || (m.Role == "tool" && m.ToolCallID == "call_1" && m.Content == "ok from read")
	}
	if !sawCall || !sawTool {
		t.Errorf("tool round trip missing from the second request: %+v", msgs)
	}

	last := f.bodies[2]
	if _, ok := last["tools"]; ok {
		t.Error("the final call sends no tools")
	}
	if !strings.Contains(string(last["response_format"]), `"type":"json_schema"`) ||
		!strings.Contains(string(last["response_format"]), `"strict":true`) {
		t.Errorf("final response_format = %s", last["response_format"])
	}
}

func TestOpenAIAPIPlainModelSendsNoEffort(t *testing.T) {
	f := newFakeOpenAI(t, openAIReply(openAIText("done")), openAIReply(openAIText(`{"done":true}`)))
	s := f.session(&fakeHost{})
	s.Model = "gpt-small"

	st, err := f.adapter().Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if _, ok := f.bodies[0]["reasoning_effort"]; ok {
		t.Error("a model not marked as reasoning must not get reasoning_effort")
	}
	if _, ok := f.bodies[0]["max_completion_tokens"]; ok {
		t.Error("a model with no output cap must not get max_completion_tokens")
	}
}

func TestOpenAIAPIStartErrors(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		reply func(http.ResponseWriter, int)
		check func(t *testing.T, err error)
	}{
		{"404", failWith(404, `{"error":{"message":"The model `+"`gpt-big`"+` does not exist","type":"invalid_request_error",`+
			`"code":"model_not_found"}}`), func(t *testing.T, err error) {
			if !errors.Is(err, ErrModelGone) {
				t.Errorf("err = %v", err)
			}
		}},
		{"401", failWith(401, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error",`+
			`"code":"invalid_api_key"}}`), func(t *testing.T, err error) {
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("err = %v", err)
			}
		}},
		{"insufficient quota", failWith(429, `{"error":{"message":"You exceeded your current quota",`+
			`"type":"insufficient_quota","code":"insufficient_quota"}}`), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapQuota || pl.Link != OpenAIAPI {
				t.Errorf("err = %v", err)
			}
		}},
		{"credits exhausted", failWith(429, `{"error":{"message":"out of credits","type":"billing_error",`+
			`"code":"credit_balance_exhausted"}}`), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapSpend {
				t.Errorf("err = %v", err)
			}
		}},
		{"spend limit under the quota type", failWith(429, `{"error":{"message":"limit","type":"insufficient_quota",`+
			`"code":"project_spend_limit_exceeded"}}`), func(t *testing.T, err error) {
			// the shared client reads the type first, so this reaches the loop as a quota cap
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapQuota {
				t.Errorf("err = %v", err)
			}
		}},
		{"organization spend limit", failWith(429, `{"error":{"message":"limit","type":"billing_error",`+
			`"code":"organization_spend_limit_exceeded"}}`), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapSpend {
				t.Errorf("err = %v", err)
			}
		}},
		{"retry-after over a minute", failWith(429, `{"error":{"message":"Rate limit reached for requests",`+
			`"type":"rate_limit_error","code":"rate_limit_exceeded"}}`, "Retry-After", "3600"), func(t *testing.T, err error) {
			var pl *ProviderLimit
			if !errors.As(err, &pl) || pl.Cap != CapWindow || pl.ResetAt.Before(now.Add(55*time.Minute)) {
				t.Errorf("err = %v limit %+v", err, pl)
			}
		}},
		{"other 400 stays final", failWith(400, `{"error":{"message":"bad","type":"invalid_request_error"}}`),
			func(t *testing.T, err error) {
				var pl *ProviderLimit
				if err == nil || errors.As(err, &pl) {
					t.Errorf("err = %v", err)
				}
			}},
	}
	for _, c := range cases {
		f := newFakeOpenAI(t, c.reply, c.reply, c.reply, c.reply, c.reply, c.reply)
		_, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}))
		c.check(t, err)
	}
}

func TestOpenAIAPIRateLimitWithoutResetRetries(t *testing.T) {
	body := `{"error":{"message":"slow","type":"rate_limit_error","code":"slow_down"}}`
	f := newFakeOpenAI(t, failWith(429, body), openAIReply(openAIText("done")), openAIReply(openAIText(`{"done":true}`)))
	st, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("a short rate limit must be retried, got %v", err)
	}
	if n := len(f.bodies); n != 3 {
		t.Errorf("%d requests, want the 429, its retry and the final call", n)
	}
}

func TestOpenAIAPIUnpricedModelNeverStarts(t *testing.T) {
	f := newFakeOpenAI(t)
	a := f.adapter()
	a.opts.Models = map[Weight]OpenAIModel{Heavy: {ID: "gpt-big", Window: 400000}}
	for _, model := range []string{"gpt-big", "gpt-unknown"} {
		s := f.session(&fakeHost{})
		s.Model = model
		if _, err := a.Start(context.Background(), s); !errors.Is(err, ErrUnpriced) {
			t.Errorf("Start(%s) = %v, want ErrUnpriced", model, err)
		}
		if _, err := a.Resume(context.Background(), s, Input{Text: "x"}); !errors.Is(err, ErrUnpriced) {
			t.Errorf("Resume(%s) = %v, want ErrUnpriced", model, err)
		}
	}
	if len(f.bodies) != 0 {
		t.Error("an unpriced model must send no request")
	}
}

func TestOpenAIAPIKeyNeverInErrors(t *testing.T) {
	f := newFakeOpenAI(t, failWith(400, `{"error":{"type":"invalid_request_error","message":"rejected key `+openAIKey+`"}}`))
	_, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}))
	if err == nil || strings.Contains(err.Error(), openAIKey) {
		t.Fatalf("error text must not hold the key: %v", err)
	}
}

func TestOpenAIAPIStreamDropRetriesFromStart(t *testing.T) {
	drop := func(w http.ResponseWriter, _ int) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"par"},"finish_reason":null}]}`+"\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			c, _, _ := hj.Hijack()
			_ = c.Close()
		}
	}
	f := newFakeOpenAI(t, drop, openAIReply(openAIText("whole")), openAIReply(openAIText(`{"done":true}`)))
	st, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) || string(lastResult(t, evs).Output) != `{"done":true}` {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIAPIResume(t *testing.T) {
	f := newFakeOpenAI(t,
		openAIReply(openAIText("first")), openAIReply(openAIText(`{"done":true}`)),
		openAIReply(openAIText("second")), openAIReply(openAIText(`{"done":true}`)),
	)
	a := f.adapter()
	s := f.session(&fakeHost{})

	st, err := a.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	_ = st.Close()

	st, err = a.Resume(context.Background(), s, Input{Text: "continue please"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	var msgs []chatMessage
	_ = json.Unmarshal(f.bodies[2]["messages"], &msgs)
	if len(msgs) < 3 || msgs[len(msgs)-1].Content != "continue please" || !strings.Contains(msgs[0].Content, "the task") {
		t.Errorf("resumed request = %+v", msgs)
	}
}

func TestOpenAIAPIProbeAndProfile(t *testing.T) {
	f := newFakeOpenAI(t)
	a := f.adapter()

	res, err := a.Probe(context.Background())
	if err != nil || !res.OK {
		t.Fatalf("Probe = %+v, %v", res, err)
	}
	if res.Models[Heavy] != "gpt-big" || res.Models[Standard] != "gpt-small" || res.Models[Light] != "gpt-small" ||
		res.Ctx != 128000 || res.CtxSource != "endpoint" {
		t.Errorf("models %v ctx %d (%s)", res.Models, res.Ctx, res.CtxSource)
	}

	p, err := a.Profile(context.Background(), Heavy)
	if err != nil || p.Ctx != 400000 || p.Slots != 3 || p.CostClass != CostPaid || p.Effort != "high" {
		t.Errorf("profile = %+v, %v", p, err)
	}
}

func TestOpenAIAPIProbeFailures(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		a, _ := NewOpenAIAPI(OpenAIAPIOptions{Models: openAIModels()})
		res, err := a.Probe(context.Background())
		if !errors.Is(err, ErrUnauthorized) || res.OK || res.Reason == "" {
			t.Errorf("Probe = %+v, %v", res, err)
		}
		if _, err := a.Profile(context.Background(), Heavy); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Profile = %v", err)
		}
	})
	t.Run("key refused", func(t *testing.T) {
		f := newFakeOpenAI(t)
		f.modelRC = 401
		if _, err := f.adapter().Probe(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Probe = %v", err)
		}
	})
	t.Run("server error", func(t *testing.T) {
		f := newFakeOpenAI(t)
		f.modelRC = 500
		if _, err := f.adapter().Probe(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Probe = %v", err)
		}
	})
	t.Run("no model configured", func(t *testing.T) {
		f := newFakeOpenAI(t)
		a := f.adapter()
		a.opts.Models = nil
		if _, err := a.Probe(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Probe = %v", err)
		}
		if _, err := a.Profile(context.Background(), Heavy); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Profile = %v", err)
		}
	})
	t.Run("model not on the key", func(t *testing.T) {
		f := newFakeOpenAI(t)
		f.listed = `{"object":"list","data":[{"id":"other"}]}`
		res, err := f.adapter().Probe(context.Background())
		if !errors.Is(err, ErrUnavailable) || res.OK {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
	t.Run("profile of a retired model", func(t *testing.T) {
		f := newFakeOpenAI(t)
		a := f.adapter()
		a.opts.Models = map[Weight]OpenAIModel{Heavy: {ID: "gpt-gone", PriceIn: 1, PriceOut: 1}}
		if _, err := a.Profile(context.Background(), Heavy); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Profile = %v", err)
		}
	})
}

func TestNewOpenAIAPIRejectsBadURL(t *testing.T) {
	if _, err := NewOpenAIAPI(OpenAIAPIOptions{BaseURL: "not a url"}); err == nil {
		t.Error("a base without a host must be refused")
	}
}

func TestChatAccKeepsRefusalAsText(t *testing.T) {
	acc := &chatAcc{}
	_ = acc.event(core.SSEEvent{Data: `{"choices":[{"delta":{"refusal":"I cannot help with that."},"finish_reason":"stop"}]}`})
	got := acc.result()
	if len(got.Content) != 1 || got.Content[0].Text != "I cannot help with that." {
		t.Errorf("refusal result = %+v", got.Content)
	}
}

func TestOpenAIModelForTakesNearestWeight(t *testing.T) {
	m, ok := openAIModelFor(openAIModels(), Light)
	if !ok || m.ID != "gpt-small" {
		t.Errorf("light = %+v %v, want the standard model", m, ok)
	}
	if _, ok := openAIModelFor(nil, Heavy); ok {
		t.Error("no models must give no model")
	}
}

// writeFile stores a test fixture file.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
