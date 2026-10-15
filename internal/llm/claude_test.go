//go:build !windows

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeScript is the stand-in for the claude program; it logs its inputs and replays out.jsonl.
const fakeScript = `#!/bin/sh
d="$FAKE_DIR"
case "$1" in
--version) cat "$d/version"; exit 0 ;;
auth) f="$d/auth.json"
  if [ -n "$CLAUDE_CONFIG_DIR" ] && [ -f "$d/auth-$(basename "$CLAUDE_CONFIG_DIR").json" ]; then f="$d/auth-$(basename "$CLAUDE_CONFIG_DIR").json"; fi
  cat "$f"; exit "$(cat "$d/auth.rc" 2>/dev/null || echo 0)" ;;
esac
printf '%s\n' "$@" > "$d/args.txt"
env > "$d/env.txt"
echo $$ > "$d/pid"
echo x >> "$d/runs.txt"
IFS= read -r first
printf '%s\n' "$first" >> "$d/stdin.txt"
while IFS= read -r line; do
  case "$line" in
    "#sleep "*) sleep "${line#\#sleep }" ;;
    *) printf '%s\n' "$line" ;;
  esac
done < "$d/out.jsonl"
if [ -f "$d/hold" ]; then sleep 30; fi
while IFS= read -r l; do printf '%s\n' "$l" >> "$d/stdin.txt"; done
`

// fake is one fake claude install in a temporary folder.
type fake struct {
	t   *testing.T
	dir string
	bin string
	cl  *Claude
}

const initNone = `{"type":"system","subtype":"init","apiKeySource":"none","session_id":"s1"}`
const subAuth = `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`

// newFake writes the fake program, puts it on PATH and returns an adapter over it.
func newFake(t *testing.T, out ...string) *fake {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fake{t: t, dir: dir, bin: bin}
	f.write("version", "2.1.295 (Claude Code)\n")
	f.write("auth.json", subAuth)
	f.lines(out...)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.cl = NewClaude(ClaudeOptions{Fallbacks: []string{filepath.Join(dir, "none", "claude")}, Env: f.env()})
	return f
}

// env is the environment handed to the fake program.
func (f *fake) env() []string {
	return append(os.Environ(), "FAKE_DIR="+f.dir, "ANTHROPIC_API_KEY=sk-planted", "CLAUDE_CODE_USE_BEDROCK=1")
}

// write stores one file in the fake's folder.
func (f *fake) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// read returns one file of the fake's folder, or empty when absent.
func (f *fake) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

// lines sets what the program prints.
func (f *fake) lines(out ...string) { f.write("out.jsonl", strings.Join(out, "\n")+"\n") }

// session returns a session folder with a brief, ready to start.
func (f *fake) session() Session {
	f.t.Helper()
	sd := f.t.TempDir()
	for name, body := range map[string]string{"brief.md": "do the task", "system.md": "format", "mcp.json": "{}"} {
		if err := os.WriteFile(filepath.Join(sd, name), []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return Session{ID: "11111111-2222-7333-8444-555555555555", Dir: sd, Agent: "builder", Link: ClaudeCLI,
		Model: "claude-sonnet-5-5", Schema: json.RawMessage(`{"type":"object"}`), Env: f.env()}
}

// drain reads events until an error and returns them with that error.
func drain(t *testing.T, st Stream) ([]StreamEvent, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var evs []StreamEvent
	for {
		ev, err := st.Next(ctx)
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}

func TestParseClaudeLine(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour).Unix()
	cases := []struct {
		name string
		line string
		kind StreamKind
	}{
		{"init", initNone, KindInit},
		{"api_retry", `{"type":"system","subtype":"api_retry","error":"rate_limit","retry_delay_ms":500}`, KindRetry},
		{"rate limit", `{"type":"rate_limit_event","rate_limit_info":{"resetsAt":` + strconv.FormatInt(reset, 10) + `}}`, KindRateLimit},
		{"assistant", `{"type":"assistant","message":{}}`, KindOutput},
		{"user", `{"type":"user","message":{}}`, KindOutput},
		{"other system", `{"type":"system","subtype":"hook"}`, KindOutput},
		{"result", `{"type":"result","subtype":"success","structured_output":{"a":1},"total_cost_usd":0.5}`, KindResult},
	}
	for _, c := range cases {
		ev, err := parseClaudeLine([]byte(c.line), "sid")
		if err != nil || ev.Kind != c.kind || string(ev.Raw) != c.line {
			t.Errorf("%s: kind %q err %v raw-equal %v, want %q", c.name, ev.Kind, err, string(ev.Raw) == c.line, c.kind)
		}
	}
	ev, _ := parseClaudeLine([]byte(cases[0].line), "sid")
	if ev.Init == nil || ev.Init.APIKeySource != "none" {
		t.Errorf("init payload = %+v", ev.Init)
	}
	ev, _ = parseClaudeLine([]byte(cases[1].line), "sid")
	if ev.Retry == nil || ev.Retry.Category != "rate_limit" || ev.Retry.RetryDelayMS != 500 {
		t.Errorf("retry payload = %+v", ev.Retry)
	}
	ev, _ = parseClaudeLine([]byte(cases[2].line), "sid")
	if ev.RateLimit == nil || ev.RateLimit.ResetAt.Unix() != reset {
		t.Errorf("rate limit payload = %+v", ev.RateLimit)
	}
	ev, _ = parseClaudeLine([]byte(cases[6].line), "sid")
	r := ev.Result
	if r == nil || string(r.Output) != `{"a":1}` || r.Usage.USDEquiv != 0.5 || r.Usage.USD != 0 || r.Handle != (ResumeHandle{ClaudeCLI, "session_id", "sid"}) {
		t.Errorf("result payload = %+v", r)
	}
	if _, err := parseClaudeLine([]byte("not json"), "sid"); err != nil {
		t.Errorf("a non-JSON line is plain output, got %v", err)
	}
}

func TestStartLaunchLine(t *testing.T) {
	f := newFake(t, initNone, `{"type":"result","structured_output":{"ok":true}}`)
	s := f.session()
	st, err := f.cl.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("drain ended with %v", err)
	}
	args := strings.Split(strings.TrimSpace(f.read("args.txt")), "\n")
	want := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--session-id", s.ID, "--model", "claude-sonnet-5-5", "--system-prompt-file", filepath.Join(s.Dir, "system.md"),
		"--tools", "", "--mcp-config", filepath.Join(s.Dir, "mcp.json"), "--strict-mcp-config",
		"--permission-mode", "dontAsk", "--permission-prompts", "none", "--allowedTools", "mcp__komodo",
		"--setting-sources", "", "--json-schema", `{"type":"object"}`}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("launch line\n got %q\nwant %q", args, want)
	}
	if !strings.Contains(f.read("stdin.txt"), `"do the task"`) {
		t.Errorf("brief not on stdin: %q", f.read("stdin.txt"))
	}
}

func TestStartEnvironment(t *testing.T) {
	f := newFake(t, initNone, `{"type":"result"}`)
	st, err := f.cl.Start(context.Background(), f.session())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, _ = drain(t, st)
	env := f.read("env.txt")
	for _, bad := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_RESUME_INTERRUPTED_TURN"} {
		if strings.Contains(env, bad) {
			t.Errorf("environment holds %s", bad)
		}
	}
	if !strings.Contains(env, "HOME="+os.Getenv("HOME")) {
		t.Errorf("real HOME missing from the claude environment")
	}
}

func TestStartBillingKillsBeforeTurnOne(t *testing.T) {
	f := newFake(t, `{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}`, `{"type":"assistant"}`)
	f.write("hold", "")
	_, err := f.cl.Start(context.Background(), f.session())
	if !errors.Is(err, ErrBilling) {
		t.Fatalf("Start = %v, want ErrBilling", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(f.read("pid")))
	deadline := time.Now().Add(5 * time.Second)
	for pid > 0 && syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pid <= 0 || syscall.Kill(pid, 0) == nil {
		t.Fatalf("process %d still alive after the billing refusal", pid)
	}
}

func TestStartRefusesLoggedOutAndPaidLogin(t *testing.T) {
	f := newFake(t, initNone)
	f.write("auth.json", `{"loggedIn":false}`)
	f.write("auth.rc", "1")
	if _, err := f.cl.Start(context.Background(), f.session()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("logged out: %v, want ErrUnauthorized", err)
	}
	f2 := newFake(t, initNone)
	f2.write("auth.json", `{"loggedIn":true,"authMethod":"api_key"}`)
	if _, err := f2.cl.Start(context.Background(), f2.session()); !errors.Is(err, ErrBilling) {
		t.Errorf("api key login: %v, want ErrBilling", err)
	}
	if f.read("runs.txt") != "" || f2.read("runs.txt") != "" {
		t.Error("the program must not start a session before the login check passes")
	}
}

func TestShortRetryDoesNotEndSession(t *testing.T) {
	f := newFake(t, initNone,
		`{"type":"system","subtype":"api_retry","error":"rate_limit","retry_delay_ms":30000}`,
		`{"type":"rate_limit_event","rate_limit_info":{"resetsAt":`+strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10)+`,"status":"rejected"}}`,
		`{"type":"result","structured_output":{}}`)
	st, _ := f.cl.Start(context.Background(), f.session())
	defer func() { _ = st.Close() }()
	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) || len(evs) != 4 || evs[len(evs)-1].Kind != KindResult {
		t.Fatalf("events %d, err %v", len(evs), err)
	}
}

func TestLongRateLimitEndsWithProviderLimit(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	f := newFake(t, initNone, `{"type":"rate_limit_event","rate_limit_info":{"resetsAt":`+strconv.FormatInt(reset.Unix(), 10)+`,"rateLimitType":"seven_day"}}`, `{"type":"assistant"}`)
	f.write("hold", "")
	s := f.session()
	st, _ := f.cl.Start(context.Background(), s)
	defer func() { _ = st.Close() }()
	_, err := drain(t, st)
	var pl *ProviderLimit
	if !errors.As(err, &pl) {
		t.Fatalf("err = %v, want ProviderLimit", err)
	}
	if pl.Link != ClaudeCLI || pl.Cap != CapWeekly || !pl.ResetAt.Equal(reset) || pl.Handle == nil || pl.Handle.Value != s.ID {
		t.Errorf("limit = %+v handle %+v", pl, pl.Handle)
	}
	if _, err := st.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("after the limit Next = %v, want ErrStreamClosed", err)
	}
}

func TestBillingErrorRetryEndsSession(t *testing.T) {
	f := newFake(t, initNone, `{"type":"system","subtype":"api_retry","error":"billing_error","retry_delay_ms":1000}`)
	f.write("hold", "")
	st, _ := f.cl.Start(context.Background(), f.session())
	defer func() { _ = st.Close() }()
	_, err := drain(t, st)
	var pl *ProviderLimit
	if !errors.As(err, &pl) || pl.Cap != CapSpend {
		t.Fatalf("err = %v, want spend ProviderLimit", err)
	}
}

func TestOverageWarnsAndSessionFinishes(t *testing.T) {
	far := strconv.FormatInt(time.Now().Add(5*time.Hour).Unix(), 10)
	f := newFake(t, initNone,
		`{"type":"rate_limit_event","rate_limit_info":{"resetsAt":`+far+`,"isUsingOverage":true}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"resetsAt":`+far+`,"status":"allowed"}}`,
		`{"type":"result","structured_output":{}}`)
	st, _ := f.cl.Start(context.Background(), f.session())
	defer func() { _ = st.Close() }()
	evs, err := drain(t, st)
	over := 0
	for _, e := range evs {
		if e.RateLimit != nil && e.RateLimit.IsUsingOverage {
			over++
		}
	}
	if !errors.Is(err, io.EOF) || over != 1 {
		t.Fatalf("err %v, overage events %d", err, over)
	}
}

func TestResultMapsUsageAndHandle(t *testing.T) {
	f := newFake(t, initNone, `{"type":"result","subtype":"success","structured_output":{"done":true},"total_cost_usd":1.25,`+
		`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}`)
	s := f.session()
	st, _ := f.cl.Start(context.Background(), s)
	defer func() { _ = st.Close() }()
	evs, _ := drain(t, st)
	r := evs[len(evs)-1].Result
	if r == nil || string(r.Output) != `{"done":true}` {
		t.Fatalf("result = %+v", r)
	}
	if *r.Usage != (Usage{TokensIn: 10, TokensOut: 20, CacheRead: 30, CacheWrite: 40, USDEquiv: 1.25}) {
		t.Errorf("usage = %+v", *r.Usage)
	}
	if r.Handle != (ResumeHandle{Link: ClaudeCLI, Kind: "session_id", Value: s.ID}) {
		t.Errorf("handle = %+v", r.Handle)
	}
}

func TestInjectReachesStdinAndCloseIsIdempotent(t *testing.T) {
	f := newFake(t, initNone)
	st, err := f.cl.Start(context.Background(), f.session())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Inject("Finish the current task and stop; 12 minutes remain"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !strings.Contains(f.read("stdin.txt"), "12 minutes remain") {
		t.Errorf("injected line missing: %q", f.read("stdin.txt"))
	}
	if err := st.Inject("late"); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("Inject after Close = %v, want ErrStreamClosed", err)
	}
}

func TestStopLadder(t *testing.T) {
	f := newFake(t, initNone)
	f.write("hold", "")
	st, err := f.cl.Start(context.Background(), f.session())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Stop(Kill); err != nil {
		t.Fatalf("Stop(Kill): %v", err)
	}
	if _, err := drain(t, st); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("a killed session has no result, err = %v", err)
	}
	if err := st.Stop(Interrupt); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("Stop on an ended session = %v, want ErrStreamClosed", err)
	}
}

func TestResumeLine(t *testing.T) {
	f := newFake(t, initNone, `{"type":"result","structured_output":{}}`)
	s := f.session()
	st, err := f.cl.Resume(context.Background(), s, Input{Text: "carry on"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, _ = drain(t, st)
	args := f.read("args.txt")
	if !strings.Contains(args, "--resume\n"+s.ID+"\n") || strings.Contains(args, "--session-id") {
		t.Errorf("resume args: %q", args)
	}
	if !strings.Contains(f.read("env.txt"), "CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1") {
		t.Error("resume environment lacks CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1")
	}
	if !strings.Contains(f.read("stdin.txt"), "carry on") || strings.Contains(f.read("stdin.txt"), "do the task") {
		t.Errorf("resume stdin: %q", f.read("stdin.txt"))
	}
}

func TestExitWithoutResult(t *testing.T) {
	f := newFake(t, initNone, `{"type":"assistant"}`)
	st, _ := f.cl.Start(context.Background(), f.session())
	_ = st.Inject("x")
	go func() { time.Sleep(100 * time.Millisecond); _ = st.Close() }()
	_, err := drain(t, st)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("a session that ends with no result must error, got %v", err)
	}
}

func TestProbe(t *testing.T) {
	f := newFake(t, initNone)
	res, err := f.cl.Probe(context.Background())
	if err != nil || !res.OK {
		t.Fatalf("Probe = %+v, %v", res, err)
	}
	if res.Path != filepath.Join(f.bin, "claude") || res.Version != "2.1.295" {
		t.Errorf("path %q version %q", res.Path, res.Version)
	}
	if res.Auth != (AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "max"}) {
		t.Errorf("auth = %+v", res.Auth)
	}
	if !strings.Contains(string(res.InitLine), `"apiKeySource":"none"`) {
		t.Errorf("init line = %s", res.InitLine)
	}
	if res.Models[Heavy] != "claude-opus-5-5" || res.Ctx <= 0 || res.CtxSource != "catalog" {
		t.Errorf("models %v ctx %d (%s)", res.Models, res.Ctx, res.CtxSource)
	}
	// A second probe of the same version spends no further turn.
	if _, err := f.cl.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(f.read("runs.txt"), "x"); n != 1 {
		t.Errorf("program started %d times for two probes of one version, want 1", n)
	}
}

func TestProbeFailures(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		cl := NewClaude(ClaudeOptions{Fallbacks: []string{filepath.Join(t.TempDir(), "claude")}})
		res, err := cl.Probe(context.Background())
		if !errors.Is(err, ErrUnavailable) || res.OK || res.Reason == "" {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
	t.Run("logged out", func(t *testing.T) {
		f := newFake(t, initNone)
		f.write("auth.json", `{"loggedIn":false}`)
		f.write("auth.rc", "1")
		res, err := f.cl.Probe(context.Background())
		if !errors.Is(err, ErrUnauthorized) || res.OK {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
	t.Run("init lacks apiKeySource", func(t *testing.T) {
		f := newFake(t, `{"type":"system","subtype":"init","session_id":"s"}`)
		res, err := f.cl.Probe(context.Background())
		if err == nil || res.OK || !strings.Contains(res.Reason, "apiKeySource") {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
}

func TestFindOrderPathThenFallbacks(t *testing.T) {
	dir := t.TempDir()
	fb := filepath.Join(dir, "fallback-claude")
	if err := os.WriteFile(fb, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	cl := NewClaude(ClaudeOptions{Fallbacks: []string{filepath.Join(dir, "missing"), fb}})
	got, err := cl.find()
	if err != nil || got != fb {
		t.Fatalf("find = %q, %v, want the first existing fallback", got, err)
	}
	if strings.Contains(strings.Join(defaultFallbacks("/home/u"), " "), "/versions/") {
		t.Error("the default list must never name the updater's versions folder")
	}
}

func TestProfile(t *testing.T) {
	f := newFake(t, initNone)
	p, err := f.cl.Profile(context.Background(), Standard)
	if err != nil {
		t.Fatal(err)
	}
	if p.CostClass != CostSubscription || p.Slots != 3 || p.Effort != "medium" || p.Ctx <= 0 {
		t.Errorf("profile = %+v", p)
	}
	f.write("auth.json", `{"loggedIn":false}`)
	f.write("auth.rc", "1")
	f.cl.resetAuth() // force a fresh login read
	if _, err := f.cl.Profile(context.Background(), Standard); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("logged out Profile = %v, want ErrUnauthorized", err)
	}
}

func TestAuthStatusCachedFor60s(t *testing.T) {
	f := newFake(t, initNone)
	now := time.Now()
	f.cl.opts.Now = func() time.Time { return now }
	if _, err := f.cl.Profile(context.Background(), Light); err != nil {
		t.Fatal(err)
	}
	f.write("auth.json", `{"loggedIn":false}`)
	f.write("auth.rc", "1")
	if _, err := f.cl.Profile(context.Background(), Light); err != nil {
		t.Errorf("within 60 s the cached login must be used: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, err := f.cl.Profile(context.Background(), Light); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("after 60 s the login is read again: %v", err)
	}
}
