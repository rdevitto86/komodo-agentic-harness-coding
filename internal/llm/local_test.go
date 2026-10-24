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
)

// fakeOllama replays an OpenAI-compatible server with Ollama's extra paths.
type fakeOllama struct {
	mu       sync.Mutex
	ollama   bool
	ctxLen   int  // context_length reported once the model is loaded
	loaded   bool // whether /api/ps lists the model
	loads    int
	chats    []map[string]json.RawMessage
	replies  []string // SSE bodies, one per chat call
	authSeen string
}

func (f *fakeOllama) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[]}`)
		case "/api/version":
			if !f.ollama {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"version":"0.12.0"}`)
		case "/api/ps":
			if f.loaded {
				_, _ = fmt.Fprintf(
					w,
					`{"models":[{"name":"Qwen3.6-27B","model":"Qwen3.6-27B","context_length":%d}]}`,
					f.ctxLen,
				)
			} else {
				_, _ = io.WriteString(w, `{"models":[]}`)
			}
		case "/api/generate":
			f.loads++
			f.loaded = true
			_, _ = io.WriteString(w, `{"done":true}`)
		case "/v1/chat/completions":
			f.authSeen = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			var m map[string]json.RawMessage
			_ = json.Unmarshal(b, &m)
			f.chats = append(f.chats, m)
			n := len(f.chats) - 1
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, f.replies[n])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLocalProbeMeasuresOllamaContext(t *testing.T) {
	f := &fakeOllama{ollama: true, ctxLen: 262144}
	p := localAdapter(t, f.server(t), LocalEndpoint{Models: map[Weight]string{Standard: "Qwen3.6-27B"}})
	res, err := p.Probe(context.Background())
	if err != nil || !res.OK {
		t.Fatalf("Probe = %+v %v", res, err)
	}
	if !res.Ollama || res.Ctx != 262144 || res.CtxSource != "ollama_ps" {
		t.Errorf("ollama %v ctx %d from %s", res.Ollama, res.Ctx, res.CtxSource)
	}
	if f.loads != 1 {
		t.Errorf("an unloaded model is loaded once with an empty request, got %d loads", f.loads)
	}
	for _, w := range []Weight{Heavy, Standard, Light} {
		if res.Models[w] != "Qwen3.6-27B" {
			t.Errorf("weight %s uses %q, want the nearest listed model", w, res.Models[w])
		}
	}
}

func TestLocalReportsSmallContextWithoutHidingIt(t *testing.T) {
	f := &fakeOllama{ollama: true, ctxLen: 4096, loaded: true}
	p := localAdapter(t, f.server(t), LocalEndpoint{Models: map[Weight]string{Standard: "Qwen3.6-27B"}})
	prof, err := p.Profile(context.Background(), Standard)
	if err != nil || prof.Ctx != 4096 || prof.CostClass != CostFree || prof.Slots != 1 {
		t.Fatalf("profile = %+v %v", prof, err)
	}
}

func TestLocalTypedContextOnlyLowers(t *testing.T) {
	f := &fakeOllama{ollama: true, ctxLen: 262144, loaded: true}
	srv := f.server(t)
	low := localAdapter(t, srv, LocalEndpoint{Models: map[Weight]string{Standard: "Qwen3.6-27B"}, Ctx: 65536})
	if p, _ := low.Profile(context.Background(), Standard); p.Ctx != 65536 {
		t.Errorf("a lower typed context = %d, want 65536", p.Ctx)
	}
	high := localAdapter(t, srv, LocalEndpoint{Models: map[Weight]string{Standard: "Qwen3.6-27B"}, Ctx: 524288})
	if p, _ := high.Profile(context.Background(), Standard); p.Ctx != 262144 {
		t.Errorf("a higher typed context = %d, want the measured 262144", p.Ctx)
	}
}

func TestLocalOtherServerNeedsContext(t *testing.T) {
	f := &fakeOllama{}
	srv := f.server(t)
	none := localAdapter(t, srv, LocalEndpoint{Models: map[Weight]string{Standard: "m"}})
	if r, err := none.Probe(context.Background()); err == nil || r.OK {
		t.Errorf("a server that is not Ollama without Ctx must fail: %+v %v", r, err)
	}
	set := localAdapter(t, srv, LocalEndpoint{Models: map[Weight]string{Standard: "m"}, Ctx: 131072})
	r, err := set.Probe(context.Background())
	if err != nil || r.Ctx != 131072 || r.CtxSource != "endpoint" || r.Ollama {
		t.Errorf("probe = %+v %v", r, err)
	}
}

func TestLocalUnreachableIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	p, _ := NewLocal(LocalOptions{
		Endpoint: LocalEndpoint{URL: url + "/v1", Models: map[Weight]string{Standard: "m"}, Ctx: 1},
	})
	if _, err := p.Probe(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Probe = %v", err)
	}
	if _, err := p.Profile(context.Background(), Standard); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Profile = %v", err)
	}
}

func TestLocalCostClass(t *testing.T) {
	for _, c := range []struct {
		ep   LocalEndpoint
		want string
	}{
		{LocalEndpoint{URL: "http://localhost:11434/v1"}, CostFree},
		{LocalEndpoint{URL: "http://127.0.0.1:1/v1"}, CostFree},
		{LocalEndpoint{URL: "http://gpu-box:11434/v1"}, CostPaid},
		{LocalEndpoint{URL: "http://gpu-box:11434/v1", SelfHosted: true}, CostFree},
		{LocalEndpoint{URL: "::bad"}, CostPaid},
	} {
		if got := c.ep.CostClass(); got != c.want {
			t.Errorf("%+v = %s, want %s", c.ep, got, c.want)
		}
	}
}

func TestLocalSessionRunsToolLoopThenSchemaCall(t *testing.T) {
	f := &fakeOllama{replies: []string{chatToolCall("c1", "read"), chatText("done"), chatText(`{"done":true}`)}}
	srv := f.server(t)
	t.Setenv("LOCAL_KEY", "local-secret-key")
	p, _ := NewLocal(LocalOptions{
		Endpoint: LocalEndpoint{
			URL:    srv.URL + "/v1",
			Models: map[Weight]string{Standard: "m"},
			Ctx:    100000,
			Key:    "env:LOCAL_KEY",
		},
	})
	host := &fakeHost{}
	s := Session{ID: "sid", Dir: t.TempDir(), Agent: "qa", Link: Local, Model: "m", Schema: json.RawMessage(
		resultSchema,
	),
		Tools: host, Profile: CapacityProfile{Ctx: 100000}, Parts: [5]string{"", "role", "", "", "the task"}}
	st, err := p.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	r := lastResult(t, evs)
	if string(r.Output) != `{"done":true}` || r.Usage.USD != 0 || r.Usage.TokensIn != 90 {
		t.Errorf("result %s usage %+v", r.Output, r.Usage)
	}
	if len(host.calls) != 1 || host.calls[0] != `read {"path":"a.go"}` {
		t.Errorf("tool calls = %v", host.calls)
	}
	if f.authSeen != "Bearer local-secret-key" {
		t.Errorf("authorization = %q", f.authSeen)
	}
	if _, ok := f.chats[0]["tools"]; !ok {
		t.Error("work turns carry tools")
	}
	last := f.chats[2]
	if _, ok := last["tools"]; ok {
		t.Error("the final call sends no tools")
	}
	if !strings.Contains(string(last["response_format"]), `"type":"json_schema"`) ||
		!strings.Contains(string(last["response_format"]), `"strict":true`) {
		t.Errorf("final response_format = %s", last["response_format"])
	}
	var msgs []chatMessage
	_ = json.Unmarshal(f.chats[1]["messages"], &msgs)
	var sawTool bool
	for _, m := range msgs {
		sawTool = sawTool || (m.Role == "tool" && m.ToolCallID == "c1" && m.Content == "ok from read")
	}
	if !sawTool {
		t.Errorf("tool result missing from the second request: %+v", msgs)
	}
}

// chatText is a streamed text reply with usage.
func chatText(text string) string {
	return fmt.Sprintf("data: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
		mustJSON(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{"content": text}, "finish_reason": "stop"}},
		}),
		`{"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":6}}`)
}

// chatTool is a streamed reply that calls a tool, arguments split across chunks.
func chatToolCall(id, name string) string {
	return fmt.Sprintf("data: %s\n\ndata: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
		mustJSON(map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index":    0,
								"id":       id,
								"function": map[string]any{"name": name, "arguments": `{"path":`},
							},
						},
					},
				},
			},
		}),
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]},`+
			`"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
}

func localAdapter(t *testing.T, srv *httptest.Server, ep LocalEndpoint) *LocalProvider {
	t.Helper()
	ep.URL = srv.URL + "/v1"
	p, err := NewLocal(LocalOptions{Endpoint: ep})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
