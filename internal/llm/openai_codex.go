package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// OpenAISubscriptionOptions tunes the OpenAI subscription adapter; every field has a working zero value.
type OpenAISubscriptionOptions struct {
	Models    map[Weight]OpenAIModel // the model per weight; a weight with none takes the nearest weight's
	Fallbacks []string               // absolute paths tried after PATH; nil means the default list
	Env       []string               // base environment of the codex program; nil means the process environment
	Slots     int                    // concurrent sessions allowed on the link; 0 means 3
	Now       func() time.Time       // clock; nil uses time.Now
	Faults    Recoverer              // guards the adapter's goroutines; nil prints a panic on standard error
}

// OpenAICodex is the subscription adapter: it drives the codex program on a ChatGPT login.
type OpenAICodex struct {
	opts   OpenAISubscriptionOptions
	mu     sync.Mutex
	logins map[string]codexLoginEntry // one login read per auth-relevant environment
}

// codexLoginEntry is one cached `codex login status` answer.
type codexLoginEntry struct {
	at  time.Time
	st  AuthStatus
	err error
}

// codexProgram is the name looked up on PATH.
const codexProgram = "codex"

// codexAuthVars are the environment variables that change which login the codex program finds.
var codexAuthVars = map[string]bool{
	"HOME":               true,
	"USER":               true,
	"LOGNAME":            true,
	"CODEX_HOME":         true,
	"CODEX_ACCESS_TOKEN": true,
	"XDG_CONFIG_HOME":    true,
	"XDG_DATA_HOME":      true,
}

// codexServerName matches the names of tool servers that are safe to write into a configuration key.
var codexServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// NewOpenAISubscription builds the subscription adapter.
func NewOpenAISubscription(opts OpenAISubscriptionOptions) *OpenAICodex {
	return &OpenAICodex{opts: opts}
}

// Link returns OpenAISubscription.
func (c *OpenAICodex) Link() Link { return OpenAISubscription }

// Probe checks the program, its version and its ChatGPT login without a model turn.
func (c *OpenAICodex) Probe(ctx context.Context) (ProbeResult, error) {
	res := ProbeResult{Link: OpenAISubscription, Models: map[Weight]string{}}
	fail := func(err error, why string) (ProbeResult, error) {
		res.Reason = why
		return res, fmt.Errorf("%s: %w", why, err)
	}

	path, err := c.find()
	if err != nil {
		return fail(ErrUnavailable, "codex program not found")
	}

	res.Path = path

	out, err := c.output(ctx, path, c.baseEnv(), "--version")
	if err != nil {
		return fail(ErrUnavailable, "codex --version failed")
	}

	if f := strings.Fields(string(out)); len(f) > 0 {
		res.Version = f[len(f)-1]
	}

	c.resetAuth()

	if res.Auth, err = c.loginStatus(ctx, c.baseEnv()); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return fail(ErrUnauthorized, "codex is logged out")
		}
		res.Reason = err.Error()
		return res, err
	}
	if Classify(OpenAISubscription, res.Auth) != CostSubscription {
		return fail(ErrBilling, "codex is logged in with "+res.Auth.AuthMethod+", not ChatGPT")
	}

	for _, w := range weightOrder {
		m, ok := openAIModelFor(c.opts.Models, w)
		if !ok {
			continue
		}

		res.Models[w] = m.ID
		if win := openAIWindow(m); res.Ctx == 0 || win < res.Ctx {
			res.Ctx = win
		}
	}

	if len(res.Models) == 0 {
		return fail(ErrUnavailable, "no OpenAI model is configured")
	}

	res.CtxSource, res.OK = "endpoint", true
	return res, nil
}

// Profile measures the capacity profile for a weight from the login and the configured model.
func (c *OpenAICodex) Profile(ctx context.Context, w Weight) (CapacityProfile, error) {
	const defaultSlots = 3

	auth, err := c.loginStatus(ctx, c.baseEnv())
	if err != nil {
		return CapacityProfile{}, err
	}

	m, ok := openAIModelFor(c.opts.Models, w)
	if !ok {
		return CapacityProfile{}, fmt.Errorf("no OpenAI model is configured: %w", ErrUnavailable)
	}

	slots := c.opts.Slots
	if slots <= 0 {
		slots = defaultSlots
	}

	return CapacityProfile{
		Ctx:       openAIWindow(m),
		Slots:     slots,
		CostClass: Classify(OpenAISubscription, auth),
		Effort:    effortFor(w),
	}, nil
}

// Start launches one session with the brief on standard input and checks the first line before returning.
func (c *OpenAICodex) Start(ctx context.Context, s Session) (Stream, error) {
	brief, err := os.ReadFile(filepath.Join(s.Dir, "brief.md"))
	if err != nil {
		return nil, fmt.Errorf("read the brief: %w", err)
	}
	return c.launch(ctx, s, "", string(brief))
}

// Resume continues the saved thread with the new input as its prompt.
func (c *OpenAICodex) Resume(ctx context.Context, s Session, in Input) (Stream, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, codexThreadFile))
	thread := strings.TrimSpace(string(b))
	if err != nil || thread == "" {
		return nil, fmt.Errorf("no saved codex thread in %s: %w", s.Dir, ErrUnavailable)
	}
	return c.launch(ctx, s, thread, in.Text)
}

// launch runs the billing check, spawns the program, sends the prompt and waits for the first line.
func (c *OpenAICodex) launch(ctx context.Context, s Session, thread, text string) (Stream, error) {
	base := s.Env
	if base == nil {
		base = c.baseEnv()
	}

	auth, err := c.loginStatus(ctx, base)
	if err != nil {
		return nil, err
	}
	if Classify(OpenAISubscription, auth) != CostSubscription {
		return nil, fmt.Errorf("login is %q, not ChatGPT: %w", auth.AuthMethod, ErrBilling)
	}

	path, err := c.find()
	if err != nil {
		return nil, err
	}

	args, err := c.launchArgs(s, thread)
	if err != nil {
		return nil, err
	}

	p, err := spawn(path, args, codexScrub(base), s.Dir)
	if err != nil {
		return nil, err
	}

	rec := recoverer(c.opts.Faults)
	st, _ := newCodexStream(p, s.ID, s.Dir, c.now, rec, s.Schema)

	go func() {
		defer rec.Recover(llmSource, nil)
		_ = p.sendPrompt(text)
	}()

	first, err := st.firstEvent(ctx)
	if err != nil {
		p.kill()
		_ = st.Close()
		return nil, err
	}

	st.pending = append(st.pending, first)
	return st, nil
}

// launchArgs builds the launch line. Every flag comes before `resume`, so one line serves a fresh start and a resume.
func (c *OpenAICodex) launchArgs(s Session, thread string) ([]string, error) {
	args := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--ignore-rules",
		"--sandbox", "read-only",
		"--model", s.Model,
		"-c", `approval_policy="never"`,
		"-c", "features.shell_tool=false",
		"-c", `web_search="disabled"`,
	}

	if sys, err := os.ReadFile(filepath.Join(s.Dir, "system.md")); err == nil && len(sys) > 0 {
		args = append(args, "-c", "developer_instructions="+tomlString(string(sys)))
	}
	if m, ok := c.modelNamed(s.Model); ok && m.Reasoning && s.Profile.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(s.Profile.Effort))
	}

	mcp, err := codexToolServers(filepath.Join(s.Dir, "mcp.json"))
	if err != nil {
		return nil, err
	}

	args = append(args, mcp...)

	if len(s.Schema) > 0 {
		path := filepath.Join(s.Dir, "output-schema.json")
		if err := os.WriteFile(path, s.Schema, 0o644); err != nil {
			return nil, fmt.Errorf("write the result schema: %w", err)
		}

		args = append(args, "--output-schema", path)
	}

	if thread != "" {
		args = append(args, "resume", thread)
	}

	return append(args, "-"), nil
}

// modelNamed returns the configured model with an identifier.
func (c *OpenAICodex) modelNamed(id string) (OpenAIModel, bool) {
	for _, m := range c.opts.Models {
		if m.ID == id {
			return m, true
		}
	}
	return OpenAIModel{}, false
}

// codexToolServers turns the session's mcp.json into configuration overrides; a missing file means no tool server.
func codexToolServers(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mcp.json: %w", err)
	}

	var cfg struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp.json: %w", err)
	}

	names := make([]string, 0, len(cfg.Servers))
	for n := range cfg.Servers {
		names = append(names, n)
	}

	slices.Sort(names)

	var out []string

	for _, n := range names {
		if !codexServerName.MatchString(n) {
			return nil, fmt.Errorf("tool server name %q is not usable as a configuration key", n)
		}

		sv := cfg.Servers[n]
		key := "mcp_servers." + n

		argList := make([]string, 0, len(sv.Args))
		for _, a := range sv.Args {
			argList = append(argList, tomlString(a))
		}

		out = append(out,
			"-c", key+".command="+tomlString(sv.Command),
			"-c", key+".args=["+strings.Join(argList, ",")+"]",
		)

		if len(sv.Env) > 0 {
			keys := make([]string, 0, len(sv.Env))
			for k := range sv.Env {
				keys = append(keys, k)
			}

			slices.Sort(keys)

			pairs := make([]string, 0, len(keys))
			for _, k := range keys {
				pairs = append(pairs, tomlString(k)+"="+tomlString(sv.Env[k]))
			}

			out = append(out, "-c", key+".env={"+strings.Join(pairs, ",")+"}")
		}
	}

	return out, nil
}

// tomlString renders s as a TOML basic string; JSON's escapes are all valid TOML escapes.
func tomlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// codexScrub returns env without the variables that would make codex prefer an API key or another server.
func codexScrub(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "CODEX_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL":
		default:
			out = append(out, kv)
		}
	}
	return out
}

// sendPrompt writes the whole prompt to standard input and closes it, which is what starts the run.
func (p *proc) sendPrompt(text string) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()

	if p.inDone {
		return ErrStreamClosed
	}

	p.inDone = true
	_, err := p.in.Write([]byte(text))
	if cerr := p.in.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write to codex: %w", err)
	}

	return nil
}

// loginStatus reads `codex login status` with the given environment and reuses the answer for 60 s.
// A cancelled or timed-out read is returned and forgotten.
func (c *OpenAICodex) loginStatus(ctx context.Context, env []string) (AuthStatus, error) {
	const authTTL = 60 * time.Second // how long one login read is reused

	c.mu.Lock()
	defer c.mu.Unlock()

	key := codexAuthKey(env)
	if e, ok := c.logins[key]; ok && c.now().Sub(e.at) < authTTL {
		return e.st, e.err
	}

	path, err := c.find()
	if err != nil {
		return AuthStatus{}, err
	}

	// the program prints its answer on standard error, so both streams are read
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, path, "login", "status")
	cmd.Env = codexScrub(env)
	out, runErr := cmd.CombinedOutput()

	if cerr := cctx.Err(); cerr != nil {
		return AuthStatus{}, fmt.Errorf("codex login status: %w", cerr)
	}

	st, aerr := parseCodexLogin(string(out), runErr)
	if c.logins == nil {
		c.logins = map[string]codexLoginEntry{}
	}

	c.logins[key] = codexLoginEntry{at: c.now(), st: st, err: aerr}
	return st, aerr
}

// parseCodexLogin maps the text of `codex login status` and its exit status to a login; any login but ChatGPT is "other".
func parseCodexLogin(out string, runErr error) (AuthStatus, error) {
	text := strings.TrimSpace(out)

	switch {
	case strings.Contains(text, "Not logged in"):
		return AuthStatus{}, fmt.Errorf("codex is logged out: %w", ErrUnauthorized)
	case runErr != nil:
		return AuthStatus{}, fmt.Errorf("codex login status failed (%v): %w", runErr, ErrUnavailable)
	case strings.Contains(text, "Logged in using ChatGPT"):
		return AuthStatus{AuthMethod: "chatgpt"}, nil
	case strings.Contains(text, "Logged in using"):
		return AuthStatus{AuthMethod: "other"}, nil
	}

	return AuthStatus{}, fmt.Errorf("codex login status gave no known answer: %w", ErrUnauthorized)
}

// resetAuth forgets every cached login read.
func (c *OpenAICodex) resetAuth() {
	c.mu.Lock()
	c.logins = map[string]codexLoginEntry{}
	c.mu.Unlock()
}

// now returns the adapter's clock.
func (c *OpenAICodex) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

// baseEnv is the environment the codex program starts with, before the scrub.
func (c *OpenAICodex) baseEnv() []string {
	if c.opts.Env != nil {
		return c.opts.Env
	}
	return os.Environ()
}

// find returns the absolute path of the codex program: PATH first, then the fallbacks in order.
func (c *OpenAICodex) find() (string, error) {
	if p, err := exec.LookPath(codexProgram); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
	}

	fallbacks := c.opts.Fallbacks
	if fallbacks == nil {
		home, _ := os.UserHomeDir()
		fallbacks = []string{
			filepath.Join(home, ".local", "bin", codexProgram),
			"/opt/homebrew/bin/codex",
			"/usr/local/bin/codex",
		}
	}

	for _, p := range fallbacks {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}

	return "", fmt.Errorf("codex program not found on PATH or in the usual folders: %w", ErrUnavailable)
}

// output runs one short codex subcommand with the scrubbed environment and returns its standard output.
func (c *OpenAICodex) output(ctx context.Context, path string, env []string, args ...string) ([]byte, error) {
	const cmdWait = 10 * time.Second // longest wait for a short codex subcommand

	cctx, cancel := context.WithTimeout(ctx, cmdWait)
	defer cancel()

	cmd := exec.CommandContext(cctx, path, args...)
	cmd.Env = codexScrub(env)
	out, err := cmd.Output()
	if cerr := cctx.Err(); cerr != nil {
		return nil, fmt.Errorf("codex %s: %w", args[0], cerr)
	}

	return out, err
}

// codexAuthKey fingerprints the auth-relevant part of an environment; a token never sits in the key as plain text.
func codexAuthKey(env []string) string {
	var rel []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); codexAuthVars[name] {
			rel = append(rel, kv)
		}
	}

	slices.Sort(rel)

	sum := sha256.Sum256([]byte(strings.Join(rel, "\x00")))
	return hex.EncodeToString(sum[:])
}
