//go:build !windows

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	apiKey    = "sk-ant-recorded-key-0001"
	sessionID = "0198f3a2-9b10-7c21-8d44-1a2b3c4d5e6f"
	finalJSON = `{"status":"done","summary":"Added Mul to calc and a test for it.","disputed":[]}`
)

// replayAPI is a recorded Messages interface: it replays scripted replies and keeps every request body.
type replayAPI struct {
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	replies []func(w http.ResponseWriter)
}

// fakeClaudeScript stands in for the claude program: it logs its inputs and replays out.jsonl.
const fakeClaudeScript = `#!/bin/sh
d="$FAKE_DIR"
case "$1" in
--version) cat "$d/version"; exit 0 ;;
auth) cat "$d/auth.json"; exit 0 ;;
esac
printf '%s\n' "$@" > "$d/args.txt"
env > "$d/env.txt"
IFS= read -r first
printf '%s\n' "$first" >> "$d/stdin.txt"
while IFS= read -r line; do
  printf '%s\n' "$line"
done < "$d/out.jsonl"
while IFS= read -r l; do printf '%s\n' "$l" >> "$d/stdin.txt"; done
`

// newReplayAPI starts the server; replies are consumed one per /v1/messages call.
func newReplayAPI(t *testing.T, replies ...func(w http.ResponseWriter)) *replayAPI {
	t.Helper()
	r := &replayAPI{replies: replies}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

// serve answers the model list, one model, and the next scripted message reply.
func (r *replayAPI) serve(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.URL.Path == "/v1/models":
		_, _ = io.WriteString(
			w,
			`{"data":[{"id":"claude-opus-5-5","max_input_tokens":1000000},{"id":"claude-sonnet-5-5",`+
				`"max_input_tokens":1000000},{"id":"claude-haiku-5-5","max_input_tokens":1000000}]}`,
		)
	case strings.HasPrefix(req.URL.Path, "/v1/models/"):
		_, _ = io.WriteString(w, `{"id":"claude-sonnet-5-5","max_input_tokens":1000000}`)
	case req.URL.Path == "/v1/messages":
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		n := len(r.bodies)
		r.bodies, r.headers = append(r.bodies, string(b)), append(r.headers, req.Header.Clone())
		var reply func(http.ResponseWriter)
		if n < len(r.replies) {
			reply = r.replies[n]
		}
		r.mu.Unlock()
		if reply == nil {
			http.Error(
				w,
				`{"error":{"type":"invalid_request_error","message":"recording exhausted"}}`,
				http.StatusBadRequest,
			)
			return
		}
		reply(w)
	default:
		http.NotFound(w, req)
	}
}

// requests returns the bodies received so far.
func (r *replayAPI) requests() ([]string, []http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...), append([]http.Header(nil), r.headers...)
}

// fakeClaude is one fake claude install on PATH in a temporary folder.
type fakeClaude struct {
	t   *testing.T
	dir string
}

// read returns one file of the fake's folder, or empty when absent.
func (f *fakeClaude) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

// replay sets the stream-json lines the program prints.
func (f *fakeClaude) replay(out ...string) { f.write("out.jsonl", strings.Join(out, "\n")+"\n") }

// write stores one file in the fake's folder.
func (f *fakeClaude) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// env is the environment handed to the fake program; the planted key proves the scrub.
func (f *fakeClaude) env() []string {
	return append(os.Environ(), "FAKE_DIR="+f.dir, "ANTHROPIC_API_KEY=sk-planted-must-not-reach-claude")
}

// toolTurn is the recorded turn in which the model reads calc/calc.go.
func toolTurn(w http.ResponseWriter) {
	sse(w, msgStart(2400),
		[2]string{
			"content_block_start",
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01ReadCalc",` +
				`"name":"read","input":{}}}`,
		},
		[2]string{
			"content_block_delta",
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		},
		[2]string{
			"content_block_delta",
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta",` +
				`"partial_json":"\"calc/calc.go\"}"}}`,
		},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		[2]string{
			"message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":38}}`,
		},
		msgStop())
}

// sse writes server-sent events in the Messages stream layout.
func sse(w http.ResponseWriter, events ...[2]string) {
	w.Header().Set("content-type", "text/event-stream")
	for _, e := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
	}
}

// msgStart opens a recorded message with its input usage.
func msgStart(in int) [2]string {
	return [2]string{
		"message_start",
		fmt.Sprintf(
			`{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant",`+
				`"usage":{"input_tokens":%d,"output_tokens":1,"cache_read_input_tokens":0,`+
				`"cache_creation_input_tokens":0}}}`,
			in,
		),
	}
}

// msgStop closes a recorded message.
func msgStop() [2]string { return [2]string{"message_stop", `{"type":"message_stop"}`} }

// textTurn is a recorded turn that ends with plain text; the text is split in two chunks.
func textTurn(text string, in, out int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		half := len(text) / 2
		sse(w, msgStart(in),
			[2]string{
				"content_block_start",
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			},
			[2]string{"content_block_delta", textDelta(text[:half])},
			[2]string{"content_block_delta", textDelta(text[half:])},
			[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			[2]string{
				"message_delta",
				fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":%d}}`, out),
			},
			msgStop())
	}
}

// textDelta renders one text_delta event body.
func textDelta(s string) string {
	b, _ := json.Marshal(s)
	return `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(b) + `}}`
}

// installClaude writes the fake program, puts it first on PATH and records a subscription login.
func installClaude(t *testing.T, out ...string) *fakeClaude {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeClaude{t: t, dir: dir}
	f.write("version", "2.1.295 (Claude Code)\n")
	f.write("auth.json", `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`)
	f.replay(out...)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}
