package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

var registerOnce sync.Once

// harness is one CLI over a temporary KOMODO_HOME with captured output.
type harness struct {
	t        *testing.T
	home     string
	terminal bool
	engine   Engine
	stdin    string
	cfg      *config.Manager
}

// newHarness builds a harness with no terminal and an engine that fails every call.
func newHarness(t *testing.T) *harness {
	t.Helper()
	registerOnce.Do(fault.RegisterEvents)
	home, err := os.MkdirTemp("", "kh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("KOMODO_HOME", home)
	t.Setenv("NO_COLOR", "")
	cfg, err := config.New(home, config.Known{}, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, home: home, engine: deadEngine{}, cfg: cfg}
}

// run executes one command line and returns the exit code and both output streams.
func (h *harness) run(args ...string) (int, string, string) {
	h.t.Helper()
	f, err := format.New(format.Options{Profile: format.ProfileStandard, Color: format.ColorOff, TurnEnd: false})
	if err != nil {
		h.t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	c := New(Deps{
		Format: f, Config: h.cfg, Engine: h.engine, Home: h.home,
		Terminal: func() bool { return h.terminal },
		Stdout:   &out, Stderr: &errOut, Stdin: strings.NewReader(h.stdin),
	})
	code := c.Run(append([]string{"komodo"}, args...))
	return code, out.String(), errOut.String()
}

// envelope runs a command with --json and decodes the envelope it printed.
func (h *harness) envelope(args ...string) (int, Envelope) {
	h.t.Helper()
	code, out, errOut := h.run(append([]string{"--json"}, args...)...)
	var e Envelope
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&e); err != nil {
		h.t.Fatalf("stdout is not one envelope: %v\nstdout=%q\nstderr=%q", err, out, errOut)
	}
	if dec.More() {
		h.t.Fatalf("stdout holds more than one JSON value: %q", out)
	}
	return code, e
}

// deadEngine fails every call, for commands that must never reach the engine.
type deadEngine struct{}

// Ensure always fails.
func (deadEngine) Ensure(context.Context) error { return os.ErrNotExist }

// Send always fails.
func (deadEngine) Send(context.Context, engine.Request) (<-chan engine.Reply, error) {
	return nil, os.ErrNotExist
}

// scriptEngine answers each request from fn and records it.
type scriptEngine struct {
	mu   sync.Mutex
	seen []engine.Request
	fn   func(engine.Request) []engine.Reply
}

// Ensure always succeeds.
func (*scriptEngine) Ensure(context.Context) error { return nil }

// Send records the request and replays the scripted frames.
func (s *scriptEngine) Send(_ context.Context, r engine.Request) (<-chan engine.Reply, error) {
	s.mu.Lock()
	s.seen = append(s.seen, r)
	s.mu.Unlock()
	ch := make(chan engine.Reply, 16)
	for _, rep := range s.fn(r) {
		rep.ID = r.ID
		ch <- rep
	}
	close(ch)
	return ch, nil
}

// okReply is a final ok frame holding v.
func okReply(v any) engine.Reply {
	b, _ := json.Marshal(v)
	return engine.Reply{Status: engine.StatusOK, Data: b, Final: true}
}

// repoDir makes a folder with a .git directory and an AGENTS.md holding the pin.
func repoDir(t *testing.T, pin string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if pin != "" {
		body := "<!-- komodo:begin min=" + pin + " -->\nx\n<!-- komodo:end -->\n"
		if err := os.WriteFile(filepath.Join(d, "AGENTS.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// shortHome makes a home folder with a path short enough for a unix socket.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "kh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}`)
