//go:build !windows

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// scrubbedErr mimics the HTTP client's error: it prints scrubbed text and unwraps to the raw error.
type scrubbedErr struct {
	msg string
	err error
}

func (e *scrubbedErr) Error() string { return e.msg }

func (e *scrubbedErr) Unwrap() error { return e.err }

func TestReviewStatusErrorsKeepCoreRedaction(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		raw := &core.StatusError{Status: status, Body: []byte("proxy says: bad key " + apiKey)}
		fail := &scrubbedErr{
			msg: fmt.Sprintf("unexpected status %d after 1 attempts: proxy says: bad key [redacted]", status),
			err: raw,
		}
		w := &fakeWire{
			script: []func(
				turnRequest,
			) (turnResult, error){func(turnRequest) (turnResult, error) { return turnResult{}, fail }},
		}
		_, err := startLoop(context.Background(), loopSession(t, w, &fakeHost{}, nil, 100000))
		if err == nil || strings.Contains(err.Error(), apiKey) {
			t.Errorf("status %d: error text leaks the key: %v", status, err)
		}
	}
}

func TestReviewOneLinePerMessageAfterMaxTokens(t *testing.T) {
	cut := func(turnRequest) (turnResult, error) {
		return turnResult{Content: []block{{Type: "text", Text: "partial"}}, Stop: stopMaxTokens}, nil
	}
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){cut, say("finished"), say(`{"done":true}`)}}
	cfg := loopSession(t, w, &fakeHost{}, nil, 100000)
	evs, err := run(t, cfg)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	path := lastResult(t, evs).Handle.Value
	lines := readLines(t, path)
	if len(lines) != 6 {
		t.Fatalf("messages.jsonl has %d lines, want 6 (one per message)", len(lines))
	}
	saved, err := loadMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	checkAlternating(t, saved)
}

func TestReviewResumeAfterInterruptWithToolResult(t *testing.T) {
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
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	path := lastResult(t, evs).Handle.Value
	if n := len(readLines(t, path)); n != 4 {
		t.Fatalf("messages.jsonl has %d lines, want 4", n)
	}
	saved, err := loadMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	checkAlternating(t, saved)
	third := saved[2].Content
	if len(third) != 2 || third[0].Type != "tool_result" || third[1].Type != "text" {
		t.Errorf("the tool result and the final prompt belong in one message: %+v", third)
	}
	w2 := &fakeWire{script: []func(turnRequest) (turnResult, error){say("more"), say(`{"done":true}`)}}
	cfg2 := cfg
	cfg2.Wire, cfg2.Messages = w2, nil
	st2, err := resumeLoop(context.Background(), cfg2, "carry on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	if _, err := drain(t, st2); !errors.Is(err, io.EOF) {
		t.Fatalf("resume err %v", err)
	}
	// The history sent on resume ends on the one new user message, after an assistant message.
	h := w2.reqs[0].Messages
	if len(h) != 5 {
		t.Fatalf("resumed history has %d messages, want 5", len(h))
	}
	checkAlternating(t, h)
}

func TestReviewCutToolCallIsAnsweredNotRunAnthropic(t *testing.T) {
	cutTool := func(w http.ResponseWriter, _ int) {
		sse(w, start(30),
			[2]string{
				"content_block_start",
				mustJSON(map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "tu1",
						"name":  "run",
						"input": map[string]any{},
					},
				}),
			},
			[2]string{
				"content_block_delta",
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta",` +
					`"partial_json":"{\"command\":\"rm -r"}}`,
			},
			[2]string{
				"message_delta",
				`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":9}}`,
			},
			stop())
	}
	f := newFakeAPI(t, cutTool, textReply("ok"), textReply(`{"done":true}`))
	host := &fakeHost{}
	st, err := f.adapter().Start(context.Background(), f.session(host, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	if len(host.calls) != 0 {
		t.Fatalf("a cut tool call was dispatched: %v", host.calls)
	}
	second := string(f.raw[1])
	if !strings.Contains(second, `"tool_use_id":"tu1"`) || !strings.Contains(second, "error: the tool call was cut off") ||
		!strings.Contains(second, "continue") {
		t.Errorf("the cut call must get an error tool_result: %s", second)
	}
}

func TestReviewCutToolCallIsAnsweredNotRunLocal(t *testing.T) {
	cut := fmt.Sprintf("data: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
		mustJSON(map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index":    0,
								"id":       "c1",
								"function": map[string]any{"name": "run", "arguments": `{"command":"rm -r`},
							},
						},
					},
				},
			},
		}),
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`)
	f := &fakeOllama{replies: []string{cut, chatText("ok"), chatText(`{"done":true}`)}}
	srv := f.server(t)
	p, _ := NewLocal(LocalOptions{
		Endpoint: LocalEndpoint{URL: srv.URL + "/v1", Models: map[Weight]string{Standard: "m"}, Ctx: 100000},
	})
	host := &fakeHost{}
	s := Session{ID: "sid", Dir: t.TempDir(), Agent: "qa", Link: Local, Model: "m", Schema: json.RawMessage(
		resultSchema,
	),
		Tools: host, Profile: CapacityProfile{Ctx: 100000}, Parts: [5]string{"", "", "", "", "the task"}}
	st, err := p.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
	if len(host.calls) != 0 {
		t.Fatalf("a cut tool call was dispatched: %v", host.calls)
	}
}

func TestReviewClaudeErrorAndInvalidResults(t *testing.T) {
	cases := []struct {
		name string
		line string
		want state.Reason
	}{
		{
			"error result",
			`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"hit the cap"}`,
			state.ReasonNoResult,
		},
		{
			"is_error on success subtype",
			`{"type":"result","subtype":"success","is_error":true,"result":"API Error"}`,
			state.ReasonNoResult,
		},
		{"schema break", `{"type":"result","subtype":"success","structured_output":{"done":"yes"}}`, state.ReasonSchemaFail},
		{"no structured output", `{"type":"result","subtype":"success","result":"plain words"}`, state.ReasonSchemaFail},
	}
	for _, c := range cases {
		f := newFake(t, initNone, c.line)
		s := f.session()
		s.Schema = json.RawMessage(resultSchema)
		st, err := f.cl.Start(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = drain(t, st)
		_ = st.Close()
		var end *EndError
		if !errors.As(err, &end) || end.Reason != c.want {
			t.Errorf("%s: err = %v, want %s", c.name, err, c.want)
		}
	}
}

func TestReviewCancelledLoginCheckIsNotCached(t *testing.T) {
	f := newFake(t, initNone)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.cl.Profile(ctx, Standard)
	if err == nil || errors.Is(err, ErrUnauthorized) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled check must return the context error, got %v", err)
	}
	if _, err := f.cl.Profile(context.Background(), Standard); err != nil {
		t.Fatalf("the failure was cached: %v", err)
	}
}

func TestReviewLoginCheckUsesTheSessionEnvironment(t *testing.T) {
	f := newFake(t, initNone, `{"type":"result","structured_output":{}}`)
	f.write("auth-other.json", `{"loggedIn":true,"authMethod":"api_key"}`)
	if _, err := f.cl.Profile(context.Background(), Standard); err != nil {
		t.Fatal(err)
	}
	s := f.session()
	s.Env = append(f.env(), "CLAUDE_CONFIG_DIR="+filepath.Join(f.dir, "other"))
	if _, err := f.cl.Start(context.Background(), s); !errors.Is(err, ErrBilling) {
		t.Fatalf("a session whose own login is an API key must be refused, got %v", err)
	}
	st, err := f.cl.Start(context.Background(), f.session())
	if err != nil {
		t.Fatalf("the base login is still a subscription: %v", err)
	}
	_ = st.Close()
}

func TestReviewOneHourCacheWritesCostTwiceTheInputPrice(t *testing.T) {
	f := newFakeAPI(t, usageReply("done"), usageReply(`{"done":true}`))
	st, err := f.adapter().Start(context.Background(), f.session(&fakeHost{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	// Sonnet 5.5: 2 USD per million input tokens; a 1 hour write costs 2x, so 0.4 USD a turn for 100,000 tokens, plus 12
	// output tokens at 10 USD per million.
	want := 2 * (0.4 + 12*10.0/1e6)
	if got := lastResult(t, evs).Usage.USD; math.Abs(got-want) > 1e-6 {
		t.Errorf("USD = %v, want %v", got, want)
	}
}

func TestReviewUnpricedPaidModelFailsClosed(t *testing.T) {
	f := newFakeAPI(t, textReply("x"))
	s := f.session(&fakeHost{}, nil)
	s.Model = "claude-mystery-9"
	_, err := f.adapter().Start(context.Background(), s)
	if !errors.Is(err, ErrUnpriced) {
		t.Fatalf("Start = %v, want ErrUnpriced", err)
	}
	if len(f.bodies) != 0 {
		t.Errorf("%d requests were sent for a model with no price", len(f.bodies))
	}
	if _, err := f.adapter().Resume(context.Background(), s, Input{Text: "x"}); !errors.Is(err, ErrUnpriced) {
		t.Errorf("Resume = %v, want ErrUnpriced", err)
	}
}

func TestReviewStubNamesTheHostLogOrSaysNotSaved(t *testing.T) {
	run1 := func(logPath string) string {
		host := &fakeHost{text: func(string) string { return strings.Repeat("x", 12000) }, log: logPath}
		var script []func(turnRequest) (turnResult, error)
		for i := 1; i <= 4; i++ {
			script = append(script, callTool(fmt.Sprintf("t%d", i), "read", `{}`))
		}
		script = append(script, say("done"), say(`{"done":true}`))
		w := &fakeWire{script: script}
		if _, err := run(t, loopSession(t, w, host, nil, 10000)); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		for _, m := range w.reqs[len(w.reqs)-1].Messages {
			for _, b := range m.Content {
				if b.Type == "tool_result" && strings.HasPrefix(b.Content, stubPrefix) {
					return b.Content
				}
			}
		}
		return ""
	}
	if got := run1("/s/out/7.log"); got != "[output of read call 1 cut: 12000 bytes in /s/out/7.log]" {
		t.Errorf("stub with a log = %q", got)
	}
	if got := run1(""); got != "[output of read call 1 cut: 12000 bytes, not saved]" {
		t.Errorf("stub without a log = %q", got)
	}
}

func TestReviewResumeKeepsToolCallNumbering(t *testing.T) {
	w := &fakeWire{
		script: []func(
			turnRequest,
		) (turnResult, error){callTool("t1", "read", `{}`), callTool("t2", "run", `{}`), say("d"), say(`{"done":true}`)},
	}
	cfg := loopSession(t, w, &fakeHost{}, nil, 100000)
	if _, err := run(t, cfg); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	l, err := newLoop(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.file.Close() }()
	if l.calls != 2 || l.info["t1"].name != "read" || l.info["t2"].n != 2 {
		t.Errorf("calls %d info %+v", l.calls, l.info)
	}
}

func TestReviewBrokenSchemaRefIsAnError(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/$defs/missing"}}}`)
	if err := validateSchema(schema, json.RawMessage(`{"a":1}`)); err == nil {
		t.Error("a $ref with no target must be a schema error")
	}
	if err := validateSchema(json.RawMessage(`{"$ref":"#/nope/deeper"}`), json.RawMessage(`1`)); err == nil {
		t.Error("a $ref through a missing key must be a schema error")
	}
}

func TestReviewRequiredEmptyFieldsSurviveReplay(t *testing.T) {
	think := block{Type: "thinking", Thinking: "", Signature: "sig"}
	b, _ := json.Marshal(think)
	if !strings.Contains(string(b), `"thinking":""`) || !strings.Contains(string(b), `"signature":"sig"`) {
		t.Errorf("thinking block = %s", b)
	}
	redacted, _ := json.Marshal(block{Type: "redacted_thinking", Data: "abc"})
	if !strings.Contains(string(redacted), `"data":"abc"`) {
		t.Errorf("redacted_thinking = %s", redacted)
	}
	use, _ := json.Marshal(block{Type: "tool_use", ID: "t", Name: "run"})
	if !strings.Contains(string(use), `"input":{}`) {
		t.Errorf("tool_use without input = %s", use)
	}
	res, _ := json.Marshal(block{Type: "tool_result", ToolUseID: "t"})
	if !strings.Contains(string(res), `"content":""`) {
		t.Errorf("tool_result with empty output = %s", res)
	}
	// An empty text block is dropped when a reply is assembled, since the interface refuses it.
	acc := &sseAcc{}
	for _, e := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"hi"}}`,
	} {
		_ = acc.event(core.SSEEvent{Data: e})
	}
	got := acc.result().Content
	if len(got) != 2 || got[0].Type != "thinking" || got[0].Signature != "sig" || got[1].Text != "hi" {
		t.Errorf("assembled content = %+v", got)
	}
}

// readLines parses messages.jsonl.
func readLines(t *testing.T, path string) []messageLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []messageLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ml messageLine
		if err := json.Unmarshal([]byte(l), &ml); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		out = append(out, ml)
	}
	return out
}

// checkAlternating fails unless the saved conversation alternates user and assistant with non-empty content.
func checkAlternating(t *testing.T, msgs []message) {
	t.Helper()
	for i, m := range msgs {
		want := "user"
		if i%2 == 1 {
			want = "assistant"
		}
		if m.Role != want || len(m.Content) == 0 {
			t.Fatalf("message %d is %s with %d blocks, want %s with content", i, m.Role, len(m.Content), want)
		}
	}
}

// usageReply is a text reply whose first-turn usage holds 100,000 one-hour cache writes.
func usageReply(text string) func(w http.ResponseWriter, n int) {
	return func(w http.ResponseWriter, _ int) {
		sse(w, [2]string{
			"message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":1,"cache_read_input_tokens":0,` +
				`"cache_creation_input_tokens":100000,"cache_creation":{"ephemeral_5m_input_tokens":0,` +
				`"ephemeral_1h_input_tokens":100000}}}}`,
		},
			[2]string{
				"content_block_start",
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			},
			[2]string{
				"content_block_delta",
				mustJSON(map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": text},
				}),
			},
			[2]string{
				"message_delta",
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`,
			},
			stop())
	}
}
