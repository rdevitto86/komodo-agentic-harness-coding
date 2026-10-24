package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// fakeWire replays scripted turns and records every request it receives.
type fakeWire struct {
	mu     sync.Mutex
	script []func(req turnRequest) (turnResult, error)
	reqs   []turnRequest
}

// turn pops the next scripted reply.
func (f *fakeWire) turn(_ context.Context, req turnRequest, h turnHooks) (turnResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := turnRequest{Final: req.Final, Messages: append([]message(nil), req.Messages...)}
	f.reqs = append(f.reqs, cp)
	if len(f.script) == 0 {
		return turnResult{}, errors.New("fake wire: script exhausted")
	}
	next := f.script[0]
	f.script = f.script[1:]
	res, err := next(cp)
	if err == nil && h.Activity != nil {
		h.Activity()
	}
	return res, err
}

// fakeHost is a tool host that records calls.
type fakeHost struct {
	mu    sync.Mutex
	calls []string
	text  func(name string) string
	log   string        // LogPath of every reply; empty means none
	gate  chan struct{} // when set, Dispatch waits for it to close
}

func (h *fakeHost) Advertised() []string { return []string{"read", "run"} }

func (h *fakeHost) Dispatch(_ context.Context, name string, args json.RawMessage) ToolReply {
	if h.gate != nil {
		<-h.gate
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, name+" "+string(args))
	if h.text != nil {
		return ToolReply{Text: h.text(name), LogPath: h.log}
	}
	return ToolReply{Text: "ok from " + name, LogPath: h.log}
}

// gateFunc adapts a function to Gate.
type gateFunc func(json.RawMessage) string

const resultSchema = `{"type":"object","required":["done"],"properties":{"done":{"type":"boolean"}},` +
	`"additionalProperties":false}`

func (g gateFunc) Check(_ context.Context, out json.RawMessage) (string, error) { return g(out), nil }

func TestLoopTextStopGetsOneFinalCall(t *testing.T) {
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){say("all work is complete"), say(`{"done":true}`)}}
	cfg := loopSession(t, w, &fakeHost{}, nil, 100000)
	evs, err := run(t, cfg)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v", err)
	}
	r := lastResult(t, evs)
	if string(r.Output) != `{"done":true}` {
		t.Errorf("output = %s", r.Output)
	}
	if len(w.reqs) != 2 || w.reqs[0].Final || !w.reqs[1].Final {
		t.Fatalf("requests: %d, final flags %v %v", len(w.reqs), w.reqs[0].Final, w.reqs[len(w.reqs)-1].Final)
	}
	if r.Handle.Kind != "messages_file" || r.Handle.Link != AnthropicAPI ||
		r.Handle.Value != filepath.Join(cfg.Session.Dir, "messages.jsonl") {
		t.Errorf("handle = %+v", r.Handle)
	}
	if r.Usage.TokensIn != 20 || r.Usage.TokensOut != 10 {
		t.Errorf("usage = %+v", r.Usage)
	}
	b, _ := os.ReadFile(r.Handle.Value)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var first struct {
		Turn int             `json:"turn"`
		Role string          `json:"role"`
		Ts   time.Time       `json:"ts"`
		C    json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.Role != "user" || len(first.C) == 0 ||
		first.Ts.IsZero() {
		t.Errorf("messages.jsonl first line %q: %v", lines[0], err)
	}
	if len(lines) != 4 {
		t.Errorf("messages.jsonl has %d lines, want 4 (user, assistant, user, assistant)", len(lines))
	}
}

func TestLoopToolCallsGoThroughHost(t *testing.T) {
	host := &fakeHost{}
	w := &fakeWire{
		script: []func(
			turnRequest,
		) (turnResult, error){callTool("t1", "read", `{"path":"a.go"}`), say("done"), say(`{"done":true}`)},
	}
	evs, err := run(t, loopSession(t, w, host, nil, 100000))
	if !errors.Is(err, io.EOF) || lastResult(t, evs) == nil {
		t.Fatalf("err %v", err)
	}
	if len(host.calls) != 1 || host.calls[0] != `read {"path":"a.go"}` {
		t.Fatalf("calls = %v", host.calls)
	}
	msgs := w.reqs[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content[0].Type != "tool_result" || last.Content[0].ToolUseID != "t1" ||
		last.Content[0].Content != "ok from read" {
		t.Errorf("tool result message = %+v", last)
	}
}

func TestLoopGateFailureContinuesSameSession(t *testing.T) {
	calls := 0
	gate := gateFunc(func(json.RawMessage) string {
		calls++
		if calls == 1 {
			return "tests still fail: TestX"
		}
		return ""
	})
	w := &fakeWire{
		script: []func(
			turnRequest,
		) (turnResult, error){say("done"), say(`{"done":true}`), say("fixed it"), say(`{"done":true}`)},
	}
	evs, err := run(t, loopSession(t, w, &fakeHost{}, gate, 100000))
	if !errors.Is(err, io.EOF) || lastResult(t, evs) == nil {
		t.Fatalf("err %v", err)
	}
	if len(w.reqs) != 4 {
		t.Fatalf("requests = %d, want 4", len(w.reqs))
	}
	m := w.reqs[2].Messages
	if got := m[len(m)-1].Content[0].Text; !strings.Contains(got, "tests still fail: TestX") {
		t.Errorf("gate feedback not sent back: %q", got)
	}
	if w.reqs[2].Final {
		t.Error("after a rejection the model may work again before the final call")
	}
}

func TestLoopTwoRejectedResultsEndCheckFail(t *testing.T) {
	gate := gateFunc(func(json.RawMessage) string { return "no" })
	w := &fakeWire{
		script: []func(turnRequest) (turnResult, error){say("d"), say(`{"done":true}`), say("d"), say(`{"done":true}`)},
	}
	_, err := run(t, loopSession(t, w, &fakeHost{}, gate, 100000))
	var end *EndError
	if !errors.As(err, &end) || end.Reason != state.ReasonCheckFail {
		t.Fatalf("err = %v, want check_fail", err)
	}
}

// schemaGate is a gate whose rejections are schema failures.
type schemaGate struct{ gateFunc }

// CapReason names the end reason for a result the gate keeps rejecting.
func (schemaGate) CapReason() state.Reason { return state.ReasonSchemaFail }

func TestLoopGateThatRejectsSchemaEndsSchemaFailNotCheckFail(t *testing.T) {
	gate := schemaGate{gateFunc(func(json.RawMessage) string { return "no" })}
	w := &fakeWire{
		script: []func(turnRequest) (turnResult, error){say("d"), say(`{"done":true}`), say("d"), say(`{"done":true}`)},
	}
	_, err := run(t, loopSession(t, w, &fakeHost{}, gate, 100000))
	var end *EndError
	if !errors.As(err, &end) || end.Reason != state.ReasonSchemaFail {
		t.Fatalf("err = %v, want schema_fail", err)
	}
}

func TestLoopSchemaRepromptOnceThenSchemaFail(t *testing.T) {
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){say("d"), say(`{"done":"yes"}`), say(`{"done":true}`)}}
	evs, err := run(t, loopSession(t, w, &fakeHost{}, nil, 100000))
	if !errors.Is(err, io.EOF) || lastResult(t, evs) == nil {
		t.Fatalf("one re-prompt should be enough, err %v", err)
	}
	m := w.reqs[2].Messages
	if !w.reqs[2].Final || !strings.Contains(m[len(m)-1].Content[0].Text, "schema") {
		t.Errorf("re-prompt = %+v final %v", m[len(m)-1], w.reqs[2].Final)
	}
	w2 := &fakeWire{script: []func(turnRequest) (turnResult, error){say("d"), say(`not json`), say(`{"done":1}`)}}
	_, err = run(t, loopSession(t, w2, &fakeHost{}, nil, 100000))
	var end *EndError
	if !errors.As(err, &end) || end.Reason != state.ReasonSchemaFail {
		t.Fatalf("err = %v, want schema_fail", err)
	}
}

func TestLoopCutReplyGetsContinueOnce(t *testing.T) {
	cut := func(turnRequest) (turnResult, error) {
		return turnResult{Content: []block{{Type: "text", Text: "partial"}}, Stop: stopMaxTokens}, nil
	}
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){cut, say("finished"), say(`{"done":true}`)}}
	if _, err := run(t, loopSession(t, w, &fakeHost{}, nil, 100000)); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	if got := w.reqs[1].Messages[len(w.reqs[1].Messages)-1].Content[0].Text; got != "continue" {
		t.Errorf("continue message = %q", got)
	}
	w2 := &fakeWire{script: []func(turnRequest) (turnResult, error){cut, cut}}
	_, err := run(t, loopSession(t, w2, &fakeHost{}, nil, 100000))
	var end *EndError
	if !errors.As(err, &end) || end.Reason != state.ReasonNoResult {
		t.Fatalf("err = %v, want no_result", err)
	}
}

func TestLoopStubsOldOutputsPastTheBudget(t *testing.T) {
	host := &fakeHost{text: func(string) string { return strings.Repeat("x", 12000) }}
	var script []func(turnRequest) (turnResult, error)
	for i := 1; i <= 5; i++ {
		script = append(script, callTool(fmt.Sprintf("t%d", i), "read", `{}`))
	}
	script = append(script, say("done"), say(`{"done":true}`))
	w := &fakeWire{script: script}
	cfg := loopSession(t, w, host, nil, 10000) // budget: min(200000, 80% of 10000) = 8000 tokens
	if _, err := run(t, cfg); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	stubbed := false
	for _, req := range w.reqs {
		if n := estimateTokens(req.Messages); n > 10000 {
			t.Errorf("a request carried %d tokens, past the context of 10000", n)
		}
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.Type == "tool_result" && b.Content == "[output of read call 1 cut: 12000 bytes, not saved]" {
					stubbed = true
				}
			}
		}
	}
	if !stubbed {
		t.Error("the oldest tool output was never stubbed")
	}
	var maxAfter int
	for _, req := range w.reqs[3:] {
		maxAfter = max(maxAfter, estimateTokens(req.Messages))
	}
	if maxAfter > 8000 {
		t.Errorf("after stubbing a request still held %d tokens, want at most the 8000 budget", maxAfter)
	}
}

func TestLoopOverContextAfterStubbingSendsNothing(t *testing.T) {
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){say("never")}}
	cfg := loopSession(t, w, &fakeHost{}, nil, 1000)
	cfg.Messages[0].Content[0].Text = strings.Repeat("y", 8000) // about 2000 tokens against a 1000-token context
	_, err := run(t, cfg)
	var end *EndError
	if !errors.As(err, &end) || end.Reason != state.ReasonNoResult {
		t.Fatalf("err = %v, want no_result", err)
	}
	if len(w.reqs) != 0 {
		t.Errorf("%d requests were sent past the context", len(w.reqs))
	}
}

func TestLoopInterruptJumpsToFinalCall(t *testing.T) {
	host := &fakeHost{gate: make(chan struct{})}
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){callTool("t1", "run", `{}`), say(`{"done":true}`)}}
	cfg := loopSession(t, w, host, nil, 100000)
	st, err := startLoop(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_ = st.Stop(Interrupt)
	close(host.gate)
	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) || lastResult(t, evs) == nil {
		t.Fatalf("err %v", err)
	}
	if len(w.reqs) != 2 || !w.reqs[1].Final {
		t.Errorf("after the interrupt the next call must be the final one: %d requests", len(w.reqs))
	}
}

func TestLoopInjectedLineReachesNextRequest(t *testing.T) {
	release := make(chan struct{})
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){
		callTool("t1", "read", `{}`),
		func(turnRequest) (turnResult, error) {
			<-release
			return callTool("t2", "read", `{}`)(turnRequest{})
		}, say("done"), say(`{"done":true}`)}}
	st, err := startLoop(context.Background(), loopSession(t, w, &fakeHost{}, nil, 100000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Inject("Finish the current task and stop; 12 minutes remain"); err != nil {
		t.Fatal(err)
	}
	close(release)
	_, _ = drain(t, st)
	var found bool
	for _, req := range w.reqs[1:] {
		m := req.Messages
		for _, b := range m[len(m)-1].Content {
			found = found || strings.Contains(b.Text, "12 minutes remain")
		}
	}
	if !found {
		t.Errorf("injected line missing from every later request")
	}
}

func TestLoopLimitMapping(t *testing.T) {
	limit := &core.LimitError{Kind: core.LimitRetryAfter, Status: 429, RetryAfter: 2 * time.Hour}
	// A limit on the first request is a refused start.
	w := &fakeWire{
		script: []func(
			turnRequest,
		) (turnResult, error){func(turnRequest) (turnResult, error) { return turnResult{}, limit }},
	}
	_, err := startLoop(context.Background(), loopSession(t, w, &fakeHost{}, nil, 100000))
	var pl *ProviderLimit
	if !errors.As(err, &pl) || pl.Cap != CapWindow || pl.Handle != nil || pl.ResetAt.IsZero() {
		t.Fatalf("Start err = %v, limit %+v", err, pl)
	}
	// A limit mid-session halts it and carries the resume handle.
	w2 := &fakeWire{script: []func(turnRequest) (turnResult, error){callTool("t1", "read", `{}`),
		func(turnRequest) (turnResult, error) {
			return turnResult{}, &core.LimitError{Kind: core.LimitSpend, Status: 429, Code: "enforced_spend_limit_reached"}
		}}}
	_, err = run(t, loopSession(t, w2, &fakeHost{}, nil, 100000))
	if !errors.As(err, &pl) || pl.Cap != CapSpend || pl.Handle == nil || pl.Handle.Kind != "messages_file" ||
		pl.Link != AnthropicAPI {
		t.Fatalf("mid-session err = %v, limit %+v", err, pl)
	}
	w3 := &fakeWire{script: []func(turnRequest) (turnResult, error){func(turnRequest) (turnResult, error) {
		return turnResult{}, &core.LimitError{Kind: core.LimitQuota, Status: 429, Code: "quota_exceeded"}
	}}}
	_, err = startLoop(context.Background(), loopSession(t, w3, &fakeHost{}, nil, 100000))
	if !errors.As(err, &pl) || pl.Cap != CapQuota {
		t.Fatalf("quota err = %v", err)
	}
}

func TestLoopStartErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"404":  {&core.StatusError{Status: 404}, ErrModelGone},
		"401":  {&core.StatusError{Status: 401}, ErrUnauthorized},
		"403":  {&core.StatusError{Status: 403}, ErrUnauthorized},
		"idle": {core.ErrStreamIdle, core.ErrStreamIdle},
	} {
		w := &fakeWire{
			script: []func(
				turnRequest,
			) (turnResult, error){func(turnRequest) (turnResult, error) { return turnResult{}, tc.err }},
		}
		_, err := startLoop(context.Background(), loopSession(t, w, &fakeHost{}, nil, 100000))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestLoopResumeContinuesSavedMessages(t *testing.T) {
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){say("done"), say(`{"done":true}`)}}
	cfg := loopSession(t, w, &fakeHost{}, nil, 100000)
	if _, err := run(t, cfg); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	w2 := &fakeWire{script: []func(turnRequest) (turnResult, error){say("more"), say(`{"done":true}`)}}
	cfg2 := cfg
	cfg2.Wire, cfg2.Messages = w2, nil
	st, err := resumeLoop(context.Background(), cfg2, "carry on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	first := w2.reqs[0].Messages
	if len(first) != 5 || first[0].Content[0].Text != "do the task" || first[len(first)-1].Content[0].Text != "carry on" {
		t.Errorf("resumed history has %d messages, last %+v", len(first), first[len(first)-1])
	}
}

func TestLoopKillStopsSession(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){callTool("t1", "read", `{}`),
		func(turnRequest) (turnResult, error) { <-block; return say("x")(turnRequest{}) }}}
	st, err := startLoop(context.Background(), loopSession(t, w, &fakeHost{}, nil, 100000))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Stop(Kill); err != nil {
		t.Fatal(err)
	}
	_, err = drain(t, st)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("a killed session has no result, err %v", err)
	}
	_ = st.Close()
	if err := st.Stop(Kill); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("Stop after Close = %v, want ErrStreamClosed", err)
	}
}

// say scripts a text-only reply.
func say(text string) func(turnRequest) (turnResult, error) {
	return func(turnRequest) (turnResult, error) {
		return turnResult{
			Content: []block{{Type: "text", Text: text}},
			Stop:    stopEnd,
			Usage:   Usage{TokensIn: 10, TokensOut: 5},
		}, nil
	}
}

// callTool scripts a reply that calls one tool.
func callTool(id, name, args string) func(turnRequest) (turnResult, error) {
	return func(turnRequest) (turnResult, error) {
		return turnResult{
			Content: []block{{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(args)}},
			Stop:    stopToolUse,
			Usage:   Usage{TokensIn: 10, TokensOut: 5},
		}, nil
	}
}

// loopSession returns a session folder and a config over the wire.
func loopSession(t *testing.T, w wire, host ToolHost, gate Gate, ctxTokens int) loopConfig {
	t.Helper()
	return loopConfig{
		Session: Session{
			ID:    "sid",
			Dir:   t.TempDir(),
			Agent: "builder",
			Link:  AnthropicAPI,
			Model: "m",
			Schema: json.RawMessage(
				resultSchema,
			),
			Tools: host, Gate: gate, Profile: CapacityProfile{Ctx: ctxTokens, Effort: "medium"}},
		Wire: w, Link: AnthropicAPI,
		Messages: []message{{Role: "user", Content: []block{{Type: "text", Text: "do the task"}}}},
	}
}

// run starts the loop and drains the stream.
func run(t *testing.T, cfg loopConfig) ([]StreamEvent, error) {
	t.Helper()
	st, err := startLoop(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	return drain(t, st)
}

func lastResult(t *testing.T, evs []StreamEvent) *Result {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].Kind != KindResult {
		t.Fatalf("no result event in %d events", len(evs))
	}
	return evs[len(evs)-1].Result
}
