//go:build !windows

package llm

// The fake program follows OpenAI's published codex exec behaviour; no test starts the real program or calls the service.
//   - exec flags (--json, --output-schema, resume, the prompt on standard input):
//     https://learn.chatgpt.com/docs/non-interactive-mode
//   - JSON line shapes (thread.started, turn.started, item.completed, turn.completed, turn.failed, error):
//     https://github.com/openai/codex/blob/main/codex-rs/exec/src/exec_events.rs
//   - exec options and the resume arguments: https://github.com/openai/codex/blob/main/codex-rs/exec/src/cli.rs
//   - `codex login status` text: https://github.com/openai/codex/blob/main/codex-rs/cli/src/login.rs
//   - usage-limit message texts: https://github.com/openai/codex/blob/main/codex-rs/protocol/src/error.rs
//   - -c keys (approval_policy, features.shell_tool, web_search, developer_instructions, model_reasoning_effort,
//     mcp_servers.<id>): https://learn.chatgpt.com/docs/config-file/config-reference
//   - API key variable for exec (CODEX_API_KEY): https://learn.chatgpt.com/docs/config-file/environment-variables

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// fakeCodexScript is the stand-in for the codex program; it logs its inputs, reads the prompt and replays out.jsonl.
const fakeCodexScript = `#!/bin/sh
d="$FAKE_DIR"
case "$1" in
--version) cat "$d/version"; exit 0 ;;
login) cat "$d/login.txt" >&2; exit "$(cat "$d/login.rc" 2>/dev/null || echo 0)" ;;
esac
printf '%s\n' "$@" > "$d/args.txt"
env > "$d/env.txt"
echo $$ > "$d/pid"
echo x >> "$d/runs.txt"
cat > "$d/stdin.txt"
cat "$d/out.jsonl"
if [ -f "$d/hold" ]; then sleep 30; fi
`

const (
	codexThreadID = "0199a213-81c0-7800-8aa1-bbab2a035a53"
	codexThread   = `{"type":"thread.started","thread_id":"` + codexThreadID + `"}`
	codexTurn     = `{"type":"turn.started"}`
	codexAnswer   = `{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"{\"done\":true}"}}`
	codexDone     = `{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,` +
		`"output_tokens":50,"reasoning_output_tokens":10}}`
	codexLoggedIn = "Logged in using ChatGPT\n"
)

// fakeCodex is one fake codex install in a temporary folder.
type fakeCodex struct {
	t   *testing.T
	dir string
	bin string
	ad  *OpenAICodex
}

// newFakeCodex writes the fake program, puts it on PATH and returns an adapter over it.
func newFakeCodex(t *testing.T, out ...string) *fakeCodex {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(fakeCodexScript), 0o755); err != nil {
		t.Fatal(err)
	}

	f := &fakeCodex{t: t, dir: dir, bin: bin}
	writeFile(t, filepath.Join(dir, "version"), "codex-cli 0.130.0\n")
	writeFile(t, filepath.Join(dir, "login.txt"), codexLoggedIn)
	f.lines(out...)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.ad = NewOpenAISubscription(OpenAISubscriptionOptions{
		Models:    openAIModels(),
		Fallbacks: []string{filepath.Join(dir, "none", "codex")},
		Env:       f.env(),
	})
	return f
}

// read returns one file of the fake's folder, or empty when absent.
func (f *fakeCodex) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

// lines sets what the program prints.
func (f *fakeCodex) lines(out ...string) {
	writeFile(f.t, filepath.Join(f.dir, "out.jsonl"), strings.Join(out, "\n")+"\n")
}

// login sets what `codex login status` prints and its exit code.
func (f *fakeCodex) login(text, rc string) {
	writeFile(f.t, filepath.Join(f.dir, "login.txt"), text)
	writeFile(f.t, filepath.Join(f.dir, "login.rc"), rc)
	f.ad.resetAuth()
}

// env is the environment handed to the fake program, with the keys that must never reach a subscription session.
func (f *fakeCodex) env() []string {
	return append(os.Environ(), "FAKE_DIR="+f.dir, "CODEX_API_KEY=sk-planted", "OPENAI_API_KEY=sk-planted",
		"OPENAI_BASE_URL=http://elsewhere.invalid")
}

// session returns a session folder with a brief, ready to start.
func (f *fakeCodex) session() Session {
	f.t.Helper()
	sd := f.t.TempDir()
	for name, body := range map[string]string{"brief.md": "do the task", "system.md": "format \"strictly\"\nplease"} {
		writeFile(f.t, filepath.Join(sd, name), body)
	}
	return Session{ID: "11111111-2222-7333-8444-555555555555", Dir: sd, Agent: "builder", Link: OpenAISubscription,
		Model: "gpt-big", Profile: CapacityProfile{Effort: "high"}, Schema: json.RawMessage(resultSchema), Env: f.env()}
}

func TestCodexLaunchLine(t *testing.T) {
	f := newFakeCodex(t, codexThread, codexTurn, codexAnswer, codexDone)
	s := f.session()
	st, err := f.ad.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("drain ended with %v", err)
	}

	args := strings.Split(strings.TrimSpace(f.read("args.txt")), "\n")
	want := []string{
		"exec", "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "--model", "gpt-big",
		"-c", `approval_policy="never"`, "-c", "features.shell_tool=false", "-c", `web_search="disabled"`,
		"-c", `developer_instructions="format \"strictly\"\nplease"`,
		"-c", `model_reasoning_effort="high"`,
		"--output-schema", filepath.Join(s.Dir, "output-schema.json"),
		"-",
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("launch line\n got %q\nwant %q", args, want)
	}
	if got := f.read("stdin.txt"); got != "do the task" {
		t.Errorf("prompt on stdin = %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Dir, "output-schema.json")); string(b) != resultSchema {
		t.Errorf("schema file = %s", b)
	}
}

func TestCodexLaunchLineCarriesToolServer(t *testing.T) {
	f := newFakeCodex(t, codexThread, codexAnswer, codexDone)
	s := f.session()
	s.Model = "gpt-small" // not a reasoning model: no effort override
	writeFile(t, filepath.Join(s.Dir, "mcp.json"), `{"mcpServers":{"komodo":{"command":"/bin/komodo",`+
		`"args":["internal","tools","--agent","builder"],"env":{"B":"2","A":"x\"y"}}}}`)

	st, err := f.ad.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, _ = drain(t, st)

	args := f.read("args.txt")
	for _, want := range []string{
		"mcp_servers.komodo.command=\"/bin/komodo\"\n",
		"mcp_servers.komodo.args=[\"internal\",\"tools\",\"--agent\",\"builder\"]\n",
		"mcp_servers.komodo.env={\"A\"=\"x\\\"y\",\"B\"=\"2\"}\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("launch line lacks %q in\n%s", want, args)
		}
	}
	if strings.Contains(args, "model_reasoning_effort") {
		t.Error("a model not marked as reasoning must not get an effort")
	}
}

func TestCodexBadToolServerFileFailsStart(t *testing.T) {
	f := newFakeCodex(t, codexThread)
	s := f.session()
	writeFile(t, filepath.Join(s.Dir, "mcp.json"), `{"mcpServers":{"bad name":{"command":"x"}}}`)
	if _, err := f.ad.Start(context.Background(), s); err == nil {
		t.Error("a tool server name that cannot be a configuration key must fail the start")
	}
	if f.read("runs.txt") != "" {
		t.Error("the program must not start")
	}
}

func TestCodexEnvironmentScrub(t *testing.T) {
	f := newFakeCodex(t, codexThread, codexAnswer, codexDone)
	st, err := f.ad.Start(context.Background(), f.session())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, _ = drain(t, st)

	env := f.read("env.txt")
	for _, bad := range []string{"CODEX_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		if strings.Contains(env, bad) {
			t.Errorf("environment holds %s", bad)
		}
	}
	if !strings.Contains(env, "HOME="+os.Getenv("HOME")) {
		t.Error("real HOME missing from the codex environment")
	}
}

func TestCodexResultMapsUsageAndHandle(t *testing.T) {
	f := newFakeCodex(t, codexThread, codexTurn,
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls",`+
			`"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"reasoning","text":"thinking"}}`,
		codexAnswer, codexDone)
	s := f.session()
	st, err := f.ad.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	r := lastResult(t, evs)
	if string(r.Output) != `{"done":true}` {
		t.Errorf("output = %s", r.Output)
	}
	if u := r.Usage; u.TokensIn != 600 || u.CacheRead != 400 || u.TokensOut != 50 || u.USD != 0 || u.USDEquiv != 0 {
		t.Errorf("usage = %+v", u)
	}
	if h := r.Handle; h.Link != OpenAISubscription || h.Kind != "session_id" || h.Value != codexThreadID {
		t.Errorf("handle = %+v", h)
	}
	if len(evs) != 6 {
		t.Errorf("%d events, want one per line, the last a result", len(evs))
	}
	if b, _ := os.ReadFile(filepath.Join(s.Dir, codexThreadFile)); strings.TrimSpace(string(b)) != r.Handle.Value {
		t.Errorf("saved thread = %q", b)
	}
}

func TestCodexResultFailures(t *testing.T) {
	cases := []struct {
		name   string
		out    []string
		reason state.Reason
	}{
		{"answer is not JSON", []string{codexThread,
			codexMessage("all finished"), codexDone}, state.ReasonSchemaFail},
		{"answer breaks the schema", []string{codexThread,
			codexMessage(`{"done":"yes"}`), codexDone}, state.ReasonSchemaFail},
		{"no answer", []string{codexThread, codexDone}, state.ReasonSchemaFail},
		{"turn failed", []string{codexThread, `{"type":"turn.failed","error":{"message":"model refused"}}`},
			state.ReasonNoResult},
	}
	for _, c := range cases {
		f := newFakeCodex(t, c.out...)
		st, err := f.ad.Start(context.Background(), f.session())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		_, err = drain(t, st)
		_ = st.Close()

		var end *EndError
		if !errors.As(err, &end) || end.Reason != c.reason {
			t.Errorf("%s: err = %v, want exit reason %s", c.name, err, c.reason)
		}
	}
}

func TestCodexLimitMessages(t *testing.T) {
	cases := []struct {
		name string
		out  []string
		cap  CapKind
	}{
		{"usage limit error event", []string{codexThread,
			`{"type":"error","message":"You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), ` +
				`visit https://chatgpt.com/settings/usage or try again at 3:05 PM."}`},
			CapWindow},
		{"usage limit as a failed turn", []string{codexThread,
			`{"type":"turn.failed","error":{"message":"You’ve hit your usage limit. Try again later."}}`}, CapWindow},
		{"spend cap", []string{codexThread,
			`{"type":"error","message":"You hit your spend cap set in your workspace. ` +
				`Increase your spend cap to continue."}`}, CapSpend},
		{"out of credits", []string{codexThread,
			`{"type":"turn.failed","error":{"message":"Your workspace is out of credits. ` +
				`Add credits to continue."}}`}, CapSpend},
		{"quota", []string{codexThread,
			`{"type":"error","message":"Quota exceeded. Check your plan and billing details."}`}, CapQuota},
	}
	for _, c := range cases {
		f := newFakeCodex(t, c.out...)
		st, err := f.ad.Start(context.Background(), f.session())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		_, err = drain(t, st)
		_ = st.Close()

		var pl *ProviderLimit
		if !errors.As(err, &pl) || pl.Cap != c.cap || pl.Link != OpenAISubscription {
			t.Fatalf("%s: err = %v, want a %s limit", c.name, err, c.cap)
		}
		if pl.Handle == nil || pl.Handle.Kind != "session_id" || pl.Handle.Value != codexThreadID {
			t.Errorf("%s: handle = %+v", c.name, pl.Handle)
		}
	}
}

func TestCodexAnswerTextNeverReadAsLimit(t *testing.T) {
	f := newFakeCodex(t, codexThread,
		codexMessage(`{"done":true,"note":"you hit your usage limit"}`),
		codexDone)
	s := f.session()
	s.Schema = json.RawMessage(`{"type":"object"}`)
	st, err := f.ad.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); !errors.Is(err, io.EOF) {
		t.Fatalf("model text must not be read as a limit: %v", err)
	}
}

func TestCodexRefusesLoggedOutAndPaidLogin(t *testing.T) {
	f := newFakeCodex(t, codexThread)
	f.login("Not logged in\n", "1")
	if _, err := f.ad.Start(context.Background(), f.session()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("logged out: %v, want ErrUnauthorized", err)
	}

	f.login("Logged in using an API key - sk-proj-***abcd\n", "0")
	if _, err := f.ad.Start(context.Background(), f.session()); !errors.Is(err, ErrBilling) {
		t.Errorf("api key login: %v, want ErrBilling", err)
	}
	if _, err := f.ad.Resume(context.Background(), f.session(), Input{Text: "x"}); err == nil {
		t.Error("a resume without a saved thread must fail so the caller starts fresh")
	}
	if f.read("runs.txt") != "" {
		t.Error("the program must not start a session before the login check passes")
	}

	p, err := f.ad.Profile(context.Background(), Standard)
	if err != nil || p.CostClass != CostPaid {
		t.Errorf("a non-ChatGPT login must profile as paid: %+v, %v", p, err)
	}
}

func TestCodexInjectIsRefusedAndCloseIsIdempotent(t *testing.T) {
	f := newFakeCodex(t, codexThread)
	f.write("hold")
	st, err := f.ad.Start(context.Background(), f.session())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Inject("12 minutes remain"); !errors.Is(err, errCodexNoInject) {
		t.Errorf("Inject on a live session = %v, want errCodexNoInject", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := st.Inject("late"); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("Inject after Close = %v, want ErrStreamClosed", err)
	}
}

// write stores one flag file in the fake's folder; an empty body is enough.
func (f *fakeCodex) write(name string) { writeFile(f.t, filepath.Join(f.dir, name), "") }

func TestCodexStopKillsAndEndedStopIsClosed(t *testing.T) {
	f := newFakeCodex(t, codexThread)
	f.write("hold")
	st, err := f.ad.Start(context.Background(), f.session())
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

func TestCodexResumeLine(t *testing.T) {
	f := newFakeCodex(t, codexThread, codexAnswer, codexDone)
	s := f.session()

	st, err := f.ad.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = drain(t, st)
	_ = st.Close()

	st, err = f.ad.Resume(context.Background(), s, Input{Text: "carry on"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	evs, err := drain(t, st)
	if !errors.Is(err, io.EOF) || lastResult(t, evs).Handle.Value == "" {
		t.Fatalf("resume drain: %v", err)
	}

	args := f.read("args.txt")
	if !strings.HasSuffix(args, "--output-schema\n"+filepath.Join(s.Dir, "output-schema.json")+
		"\nresume\n0199a213-81c0-7800-8aa1-bbab2a035a53\n-\n") {
		t.Errorf("resume args:\n%s", args)
	}
	if got := f.read("stdin.txt"); got != "carry on" {
		t.Errorf("resume prompt = %q", got)
	}
}

func TestCodexExitWithoutResult(t *testing.T) {
	f := newFakeCodex(t, codexThread, codexTurn)
	st, err := f.ad.Start(context.Background(), f.session())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := drain(t, st); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("a session that ends with no result must error, got %v", err)
	}
}

func TestCodexNoOutputAtAllIsUnavailable(t *testing.T) {
	f := newFakeCodex(t)
	writeFile(t, filepath.Join(f.dir, "out.jsonl"), "")
	if _, err := f.ad.Start(context.Background(), f.session()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a program that prints nothing = %v, want ErrUnavailable", err)
	}
}

func TestCodexProbe(t *testing.T) {
	f := newFakeCodex(t)
	res, err := f.ad.Probe(context.Background())
	if err != nil || !res.OK {
		t.Fatalf("Probe = %+v, %v", res, err)
	}
	if res.Path != filepath.Join(f.bin, "codex") || res.Version != "0.130.0" || res.Auth.AuthMethod != "chatgpt" {
		t.Errorf("path %q version %q auth %+v", res.Path, res.Version, res.Auth)
	}
	if res.Models[Heavy] != "gpt-big" || res.Models[Light] != "gpt-small" || res.Ctx != 128000 ||
		res.CtxSource != "endpoint" {
		t.Errorf("models %v ctx %d (%s)", res.Models, res.Ctx, res.CtxSource)
	}
	if f.read("runs.txt") != "" {
		t.Error("a probe must not start a model session")
	}
}

func TestCodexProbeFailures(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		ad := NewOpenAISubscription(OpenAISubscriptionOptions{
			Models: openAIModels(), Fallbacks: []string{filepath.Join(t.TempDir(), "codex")},
		})
		res, err := ad.Probe(context.Background())
		if !errors.Is(err, ErrUnavailable) || res.OK || res.Reason == "" {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
	t.Run("logged out", func(t *testing.T) {
		f := newFakeCodex(t)
		f.login("Not logged in\n", "1")
		res, err := f.ad.Probe(context.Background())
		if !errors.Is(err, ErrUnauthorized) || res.OK {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
	t.Run("api key login", func(t *testing.T) {
		f := newFakeCodex(t)
		f.login("Logged in using an API key - sk-proj-***abcd\n", "0")
		res, err := f.ad.Probe(context.Background())
		if !errors.Is(err, ErrBilling) || res.OK {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
	t.Run("status command fails", func(t *testing.T) {
		f := newFakeCodex(t)
		f.login("Error checking login status: boom\n", "1")
		if _, err := f.ad.Probe(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Probe = %v, want ErrUnavailable", err)
		}
	})
	t.Run("no model configured", func(t *testing.T) {
		f := newFakeCodex(t)
		f.ad.opts.Models = nil
		res, err := f.ad.Probe(context.Background())
		if !errors.Is(err, ErrUnavailable) || res.OK {
			t.Errorf("Probe = %+v, %v", res, err)
		}
	})
}

func TestCodexProfile(t *testing.T) {
	f := newFakeCodex(t)
	p, err := f.ad.Profile(context.Background(), Heavy)
	if err != nil {
		t.Fatal(err)
	}
	if p.CostClass != CostSubscription || p.Slots != 3 || p.Effort != "high" || p.Ctx != 400000 {
		t.Errorf("profile = %+v", p)
	}

	f.login("Not logged in\n", "1")
	if _, err := f.ad.Profile(context.Background(), Heavy); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("logged out Profile = %v, want ErrUnauthorized", err)
	}
}

func TestCodexLoginCachedFor60s(t *testing.T) {
	f := newFakeCodex(t)
	now := time.Now()
	f.ad.opts.Now = func() time.Time { return now }
	if _, err := f.ad.Profile(context.Background(), Light); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.dir, "login.txt"), "Not logged in\n")
	writeFile(t, filepath.Join(f.dir, "login.rc"), "1")
	if _, err := f.ad.Profile(context.Background(), Light); err != nil {
		t.Errorf("within 60 s the cached login must be used: %v", err)
	}

	now = now.Add(61 * time.Second)
	if _, err := f.ad.Profile(context.Background(), Light); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("after 60 s the login is read again: %v", err)
	}
}

func TestParseCodexLoginTexts(t *testing.T) {
	cases := []struct {
		out    string
		failed bool
		method string
		err    error
	}{
		{"Logged in using ChatGPT\n", false, "chatgpt", nil},
		{"Logged in using an API key - sk-***\n", false, "other", nil},
		{"Logged in using access token\n", false, "other", nil},
		{"Not logged in\n", true, "", ErrUnauthorized},
		{"Error checking login status: x\n", true, "", ErrUnavailable},
		{"", false, "", ErrUnauthorized},
	}
	for _, c := range cases {
		var runErr error
		if c.failed {
			runErr = errors.New("exit status 1")
		}

		st, err := parseCodexLogin(c.out, runErr)
		if st.AuthMethod != c.method || !errors.Is(err, c.err) && (err != nil || c.err != nil) {
			t.Errorf("parseCodexLogin(%q) = %+v, %v", c.out, st, err)
		}
	}
}

func TestTomlStringEscapes(t *testing.T) {
	if got := tomlString("a\"b\\c\nd<e>&"); got != `"a\"b\\c\nd<e>&"` {
		t.Errorf("tomlString = %s", got)
	}
}

func TestClassifyOpenAISubscription(t *testing.T) {
	if Classify(OpenAISubscription, AuthStatus{AuthMethod: "chatgpt"}) != CostSubscription ||
		Classify(OpenAISubscription, AuthStatus{AuthMethod: "other"}) != CostPaid ||
		Classify(ClaudeCLI, AuthStatus{AuthMethod: "chatgpt"}) != CostPaid {
		t.Error("only a ChatGPT login on the OpenAI subscription link is a subscription")
	}
}

// codexMessage is the line of a finished agent message.
func codexMessage(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{"id": "item_9", "type": "agent_message", "text": text},
	})
	return string(b)
}
