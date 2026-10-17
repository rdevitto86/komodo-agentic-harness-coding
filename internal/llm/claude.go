package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ClaudeOptions tunes the subscription adapter; every field has a working zero value.
type ClaudeOptions struct {
	// absolute paths tried after PATH; nil means the default list
	Fallbacks []string
	// base environment of the claude program; nil means the process environment
	Env       []string
	Slots     int                        // concurrent sessions allowed on the link; 0 means 3
	InitLines map[string]json.RawMessage // system/init lines already recorded per program version
	Now       func() time.Time           // clock; nil uses time.Now
	Catalog   *Catalog                   // model catalog; nil loads the embedded one
	ProbeWait time.Duration              // longest wait for the probe's init line; 0 means 30 s
	// guards the adapter's goroutines; nil prints a panic on standard error
	Faults Recoverer
}

// Claude is the subscription adapter: it drives the claude program.
type Claude struct {
	opts   ClaudeOptions
	cat    *Catalog
	mu     sync.Mutex
	logins map[string]loginEntry // one login read per auth-relevant environment
	inits  map[string]json.RawMessage
}

// loginEntry is one cached `claude auth status` answer.
type loginEntry struct {
	at  time.Time
	st  AuthStatus
	err error
}

// proc is one running claude program.
type proc struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	errBuf *tailBuffer
	wmu    sync.Mutex
	inDone bool
	exited chan struct{}
	exitEr error
}

// tailBuffer keeps the last 4 KB written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

// claudeProgram is the name looked up on PATH.
const claudeProgram = "claude"

// authVars are the environment variables that change which login the claude program finds.
var authVars = map[string]bool{
	"HOME":                    true,
	"USER":                    true,
	"LOGNAME":                 true,
	"CLAUDE_CONFIG_DIR":       true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"XDG_CONFIG_HOME":         true,
	"XDG_DATA_HOME":           true,
	"XDG_STATE_HOME":          true,
	"XDG_CACHE_HOME":          true,
}

// errNoResult is the cause of a stream that ended without its result event.
var errNoResult = errors.New("claude exited without a result")

// NewClaude builds the subscription adapter.
func NewClaude(opts ClaudeOptions) *Claude {
	c := &Claude{opts: opts, cat: opts.Catalog, inits: map[string]json.RawMessage{}}
	if c.cat == nil {
		c.cat, _ = LoadCatalog()
	}

	for v, l := range opts.InitLines {
		c.inits[v] = l
	}

	return c
}

// Link returns ClaudeCLI.
func (c *Claude) Link() Link { return ClaudeCLI }

// Profile measures the capacity profile for a weight from the login and the catalog.
func (c *Claude) Profile(ctx context.Context, w Weight) (CapacityProfile, error) {
	// Timing limits of the subscription adapter.
	const defaultSlot = 3 // sessions allowed on the link when none is set

	auth, err := c.loginStatus(ctx, c.baseEnv())
	if err != nil {
		return CapacityProfile{}, err
	}

	slots := c.opts.Slots
	if slots <= 0 {
		slots = defaultSlot
	}

	ctxTokens := DefaultWindow
	if e, ok := c.cat.Default(w); ok {
		ctxTokens, _ = windowFor(c.cat, e.ID)
	}

	return CapacityProfile{
		Ctx:       ctxTokens,
		Slots:     slots,
		CostClass: Classify(ClaudeCLI, auth),
		Effort:    effortFor(w),
	}, nil
}

// Probe checks the program, its login and its init line without a model turn beyond the first per
// program version.
func (c *Claude) Probe(ctx context.Context) (ProbeResult, error) {
	res := ProbeResult{Link: ClaudeCLI, Models: c.cat.Models(), CtxSource: "catalog"}
	fail := func(err error, why string) (ProbeResult, error) {
		res.Reason = why

		return res, fmt.Errorf("%s: %w", why, err)
	}

	path, err := c.find()
	if err != nil {
		return fail(ErrUnavailable, "claude program not found")
	}

	res.Path = path

	out, err := c.output(ctx, path, c.baseEnv(), "--version")
	if err != nil {
		return fail(ErrUnavailable, "claude --version failed")
	}

	res.Version, _, _ = strings.Cut(strings.TrimSpace(string(out)), " ")

	c.resetAuth()

	if res.Auth, err = c.loginStatus(ctx, c.baseEnv()); err != nil {
		if !errors.Is(err, ErrUnauthorized) {
			res.Reason = err.Error()

			return res, err
		}

		return fail(ErrUnauthorized, "claude is logged out")
	}

	res.Ctx = c.smallestWindow()

	line, err := c.initLine(ctx, path, res.Version)
	if err != nil {
		return fail(err, "could not read the init line")
	}

	res.InitLine = line

	var fields map[string]json.RawMessage

	_ = json.Unmarshal(line, &fields)

	src, ok := fields["apiKeySource"]
	if !ok {
		return fail(
			ErrUnavailable,
			"init line has no apiKeySource field in program version "+res.Version,
		)
	}

	var key string

	_ = json.Unmarshal(src, &key)
	if err := CheckInit(Init{APIKeySource: key}); err != nil {
		return fail(err, "probe session did not use the subscription login")
	}

	res.OK = true

	return res, nil
}

// Start launches one session and checks its first line before returning.
func (c *Claude) Start(ctx context.Context, s Session) (Stream, error) {
	brief, err := os.ReadFile(filepath.Join(s.Dir, "brief.md"))
	if err != nil {
		return nil, fmt.Errorf("read the brief: %w", err)
	}

	return c.launch(ctx, s, false, string(brief))
}

// Resume continues a saved session with the new input on standard input.
func (c *Claude) Resume(ctx context.Context, s Session, in Input) (Stream, error) {
	return c.launch(ctx, s, true, in.Text)
}

// resetAuth forgets every cached login read.
func (c *Claude) resetAuth() {
	c.mu.Lock()
	c.logins = map[string]loginEntry{}
	c.mu.Unlock()
}

// now returns the adapter's clock.
func (c *Claude) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}

	return time.Now()
}

// baseEnv is the environment the claude program starts with, before the scrub.
func (c *Claude) baseEnv() []string {
	if c.opts.Env != nil {
		return c.opts.Env
	}

	return os.Environ()
}

// find returns the absolute path of the claude program: PATH first, then the fallbacks in order.
func (c *Claude) find() (string, error) {
	if p, err := exec.LookPath(claudeProgram); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
	}

	fallbacks := c.opts.Fallbacks
	if fallbacks == nil {
		home, _ := os.UserHomeDir()
		fallbacks = defaultFallbacks(home)
	}

	for _, p := range fallbacks {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}

	return "", fmt.Errorf(
		"claude program not found on PATH or in the usual folders: %w",
		ErrUnavailable,
	)
}

// output runs one short claude subcommand with the scrubbed environment and returns its standard
// output.
func (c *Claude) output(
	ctx context.Context,
	path string,
	env []string,
	args ...string,
) ([]byte, error) {
	const cmdWait = 10 * time.Second // longest wait for a short claude subcommand

	cctx, cancel := context.WithTimeout(ctx, cmdWait)
	defer cancel()

	cmd := exec.CommandContext(cctx, path, args...)
	cmd.Env = Scrub(env)
	out, err := cmd.Output()

	if cerr := cctx.Err(); cerr != nil {
		return nil, fmt.Errorf("claude %s: %w", args[0], cerr)
	}

	return out, err
}

// loginStatus reads `claude auth status --json` with the given environment and reuses the answer
// for 60 s. Only a real answer is kept; a cancelled or timed-out read is returned and forgotten.
func (c *Claude) loginStatus(ctx context.Context, env []string) (AuthStatus, error) {
	const authTTL = 60 * time.Second // how long one login read is reused

	c.mu.Lock()
	defer c.mu.Unlock()

	key := authKey(env)
	if e, ok := c.logins[key]; ok && c.now().Sub(e.at) < authTTL {
		return e.st, e.err
	}

	path, err := c.find()
	if err != nil {
		return AuthStatus{}, err
	}

	out, runErr := c.output(ctx, path, env, "auth", "status", "--json")
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return AuthStatus{}, runErr
	}

	var raw struct {
		LoggedIn *bool `json:"loggedIn"`
		AuthStatus
	}

	parseErr := json.Unmarshal(out, &raw)

	var (
		st   AuthStatus
		aerr error
	)

	switch {
	case parseErr != nil && runErr != nil:
		aerr = fmt.Errorf("claude auth status failed: %w", ErrUnauthorized)
	case parseErr != nil:
		aerr = fmt.Errorf("claude auth status gave no JSON: %w", ErrUnauthorized)
	case (raw.LoggedIn != nil && !*raw.LoggedIn) || raw.AuthMethod == "":
		aerr = fmt.Errorf("claude is logged out: %w", ErrUnauthorized)
	default:
		st = raw.AuthStatus
	}

	if c.logins == nil {
		c.logins = map[string]loginEntry{}
	}

	c.logins[key] = loginEntry{at: c.now(), st: st, err: aerr}

	return st, aerr
}

// smallestWindow is the smallest context window among the default models.
func (c *Claude) smallestWindow() int {
	least := 0
	for _, id := range c.cat.Models() {
		if w, _ := windowFor(c.cat, id); least == 0 || w < least {
			least = w
		}
	}

	if least == 0 {
		return DefaultWindow
	}

	return least
}

// initLine returns the system/init line recorded for a program version, reading it from a one-word
// session the first time.
func (c *Claude) initLine(ctx context.Context, path, version string) (json.RawMessage, error) {
	const probeWait = 30 * time.Second // longest wait for the probe's init line

	c.mu.Lock()
	if l, ok := c.inits[version]; ok {
		c.mu.Unlock()

		return l, nil
	}
	c.mu.Unlock()

	model := "claude-haiku-5-5"
	if e, ok := c.cat.Default(Light); ok {
		model = e.ID
	}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--model", model,
		"--setting-sources", "",
		"--tools", "",
	}

	p, err := spawn(path, args, Scrub(c.baseEnv()), "")
	if err != nil {
		return nil, err
	}
	defer p.kill()

	wait := c.opts.ProbeWait
	if wait <= 0 {
		wait = probeWait
	}

	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	rec := recoverer(c.opts.Faults)
	go func() {
		defer rec.Recover(llmSource, nil)

		_ = p.writeUser("ok")
	}()

	lines := make(chan []byte, 1)

	go func() {
		defer rec.Recover(llmSource, nil)
		defer func() {
			_, _ = io.Copy(io.Discard, p.out)
			_ = p.cmd.Wait()
		}()

		for {
			l, err := p.out.ReadBytes('\n')
			if t := bytes.TrimSpace(l); len(t) > 0 {
				if ev, _ := parseClaudeLine(t, ""); ev.Kind == KindInit {
					lines <- t

					return
				}
			}

			if err != nil {
				close(lines)

				return
			}
		}
	}()

	select {
	case l, ok := <-lines:
		if !ok {
			return nil, fmt.Errorf(
				"probe session ended before its init line%s: %w",
				p.stderrTail(),
				ErrUnavailable,
			)
		}

		line := json.RawMessage(append([]byte(nil), l...))

		c.mu.Lock()
		c.inits[version] = line
		c.mu.Unlock()

		return line, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("probe session sent no init line: %w", ErrUnavailable)
	}
}

// launch runs the billing check, spawns the program, sends the message and checks the init line.
func (c *Claude) launch(ctx context.Context, s Session, resume bool, text string) (Stream, error) {
	base := s.Env
	if base == nil {
		base = c.baseEnv()
	}

	auth, err := c.loginStatus(ctx, base)
	if err != nil {
		return nil, err
	}

	if Classify(ClaudeCLI, auth) != CostSubscription {
		return nil, fmt.Errorf("login is %q, not a subscription: %w", auth.AuthMethod, ErrBilling)
	}

	path, err := c.find()
	if err != nil {
		return nil, err
	}

	env := Scrub(base)
	if resume {
		env = append(env, "CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1")
	}

	p, err := spawn(path, launchArgs(s, resume), env, s.Dir)
	if err != nil {
		return nil, err
	}

	rec := recoverer(c.opts.Faults)

	st := newClaudeStream(p, s.ID, c.now, rec, s.Schema)
	go func() {
		defer rec.Recover(llmSource, nil)

		_ = p.writeUser(text)
	}()

	first, err := st.firstEvent(ctx)
	if err == nil && (first.Kind != KindInit || first.Init == nil) {
		err = fmt.Errorf("first line is not system/init: %w", ErrBilling)
	}

	if err == nil {
		err = CheckInit(*first.Init)
	}

	if err != nil {
		p.kill()

		_ = st.Close()

		return nil, err
	}

	st.pending = append(st.pending, first)

	return st, nil
}

// writeUser sends one user message on standard input.
func (p *proc) writeUser(text string) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()

	if p.inDone {
		return ErrStreamClosed
	}

	if _, err := p.in.Write(userMessage(text)); err != nil {
		return fmt.Errorf("write to claude: %w", err)
	}

	return nil
}

// closeInput closes standard input once.
func (p *proc) closeInput() {
	p.wmu.Lock()
	defer p.wmu.Unlock()

	if !p.inDone {
		p.inDone = true
		_ = p.in.Close()
	}
}

// kill ends the whole process tree; a second signal catches a child forked while the first was in
// flight.
func (p *proc) kill() {
	_ = signalProc(p.cmd.Process, Kill)

	time.AfterFunc(200*time.Millisecond, func() { _ = signalProc(p.cmd.Process, Kill) })
	p.closeInput()
}

// stderrTail returns the program's last error output, ready to append to a message.
func (p *proc) stderrTail() string {
	if t := strings.TrimSpace(p.errBuf.String()); t != "" {
		return " (" + t + ")"
	}

	return ""
}

// Write appends p and trims the front to the last 4 KB.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}

	return len(p), nil
}

// String returns the kept text.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return string(t.b)
}

// authKey fingerprints the auth-relevant part of an environment; the token never sits in the key as
// plain text.
func authKey(env []string) string {
	var rel []string

	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); authVars[name] {
			rel = append(rel, kv)
		}
	}

	slices.Sort(rel)
	sum := sha256.Sum256([]byte(strings.Join(rel, "\x00")))

	return hex.EncodeToString(sum[:])
}

// defaultFallbacks lists the places tried after PATH; never the updater's versions folder.
func defaultFallbacks(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin", claudeProgram),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
	}
}

// effortFor returns the fixed effort of a weight on the model links.
func effortFor(w Weight) string {
	switch w {
	case Heavy:
		return "high"
	case Light:
		return "low"
	}

	return "medium"
}

// windowFor returns the context window the catalog lists for a model, or the default window.
func windowFor(cat *Catalog, model string) (int, string) {
	if e, ok := cat.Lookup(model); ok && e.Window > 0 {
		return e.Window, "catalog"
	}

	return DefaultWindow, "default"
}

// launchArgs builds the launch line; a resume swaps --session-id for --resume.
func launchArgs(s Session, resume bool) []string {
	id := "--session-id"
	if resume {
		id = "--resume"
	}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		id, s.ID,
		"--model", s.Model,
		"--system-prompt-file", filepath.Join(s.Dir, "system.md"),
		"--tools", "",
		"--mcp-config", filepath.Join(s.Dir, "mcp.json"),
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--allowedTools", "mcp__komodo",
		"--setting-sources", "",
	}
	if len(s.Schema) > 0 {
		args = append(args, "--json-schema", string(s.Schema))
	}

	return args
}

// spawn starts the program in its own process group with piped input and output.
func spawn(path string, args, env []string, dir string) (*proc, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = dir
	setProcAttrs(cmd)
	p := &proc{cmd: cmd, errBuf: &tailBuffer{}, exited: make(chan struct{})}
	cmd.Stderr = p.errBuf

	var err error
	if p.in, err = cmd.StdinPipe(); err != nil {
		return nil, fmt.Errorf("open claude input: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open claude output: %w", err)
	}

	p.out = bufio.NewReaderSize(stdout, 64<<10)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %v: %w", err, ErrUnavailable)
	}

	return p, nil
}

// userMessage renders one stream-json user message line.
func userMessage(text string) []byte {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	}
	b, _ := json.Marshal(msg)

	return append(b, '\n')
}
