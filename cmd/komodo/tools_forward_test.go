package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// hostConfiner runs a session's commands directly on the host.
type hostConfiner struct{ dir string }

// SetMasked ignores the masked list.
func (hostConfiner) SetMasked(string, []string) error { return nil }

// Command builds a host command in the worktree.
func (h hostConfiner) Command(ctx context.Context, _ string, c sandbox.Command) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = h.dir

	return cmd, nil
}

// fixedGit answers the diff and git_read tools with fixed text.
type fixedGit struct{}

// Diff returns a fixed diff.
func (fixedGit) Diff(context.Context, []string) (string, error) {
	return "diff --git a/a.txt b/a.txt", nil
}

// ReadOnly returns a fixed log line.
func (fixedGit) ReadOnly(_ context.Context, command string, _ []string) (string, error) {
	return "git " + command + " output", nil
}

// toolEngine is a real engine serving tool requests for the sessions in its routes.
type toolEngine struct {
	socket   string
	routes   *task.ToolRoutes
	worktree string
	bound    *task.Bound
	done     []string
	found    []task.Submitted
	mu       sync.Mutex
}

// newToolEngine binds all eleven built-in tools over fakes, routes them to session "s-1" and serves them on a real engine.
func newToolEngine(t *testing.T) *toolEngine {
	t.Helper()

	dir, err := os.MkdirTemp("", "kt")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	wt := filepath.Join(dir, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if wt, err = filepath.EvalSymlinks(wt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	x := &toolEngine{socket: filepath.Join(dir, engine.SocketName), routes: task.NewToolRoutes(), worktree: wt}
	env := &task.Env{
		Worktree: wt,
		Dir:      filepath.Join(dir, "session"),
		Confiner: hostConfiner{dir: wt},
		Instance: "i1",
		Commands: map[string][]string{"build": {"echo", "built"}},
		Git:      fixedGit{},
		Skill:    func(ref string) (string, error) { return "skill text of " + ref, nil },
		Lint:     func(string) []string { return nil },
		Done: func(_ context.Context, id string) (int, string, error) {
			x.mu.Lock()
			x.done = append(x.done, id)
			x.mu.Unlock()
			return 0, "all checks passed", nil
		},
		OnFinding: func(s task.Submitted) { x.mu.Lock(); x.found = append(x.found, s); x.mu.Unlock() },
	}

	if err := os.MkdirAll(env.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	tools := []string{
		"read", "search", "edit", "write", "run", "diff", "git_read", "skill", "submit_finding", "task_done",
		"lint_plan",
	}
	perm := task.Permissions{
		Tools:      tools,
		RunClasses: []string{"build"},
		Guard:      []task.RowID{"R1", "B1"},
	}

	rows, err := task.LoadRows(filepath.Join(dir, "home"))
	if err != nil {
		t.Fatal(err)
	}

	g, err := task.NewGuard(
		rows,
		task.Binding{Agent: "builder", Permissions: perm, Worktree: wt, Files: []string{"**"}},
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if x.bound, err = task.NewRegistry(env).Bind(tools, g); err != nil {
		t.Fatal(err)
	}

	x.routes.Add("s-1", x.bound)

	registerAllEvents.Do(registerEvents)
	eng := engine.NewEngine(
		engine.Deps{Bus: bus.New(bus.Options{}), Home: dir, Tools: x.routes},
		x.socket,
	)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)

	go func() { served <- eng.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("the engine did not stop")
		}
	})

	deadline := time.Now().Add(5 * time.Second)

	for {
		// This connection stays open: with no run live, the engine's exit rule ends it once the last connection closes.
		c, err := net.Dial("unix", x.socket)
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine never answered on %s: %v", x.socket, err)
		}

		time.Sleep(10 * time.Millisecond)
	}

	return x
}

// mcpPipe talks to ServeMCP over pipes the way the program talks to the tool server.
type mcpPipe struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	id  int
}

// serveRelay runs ServeMCP over the relay to the engine's socket and returns the client end.
func serveRelay(t *testing.T, socket, session string) *mcpPipe {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	go func() {
		_ = task.ServeMCP(context.Background(), inR, outW, &engineRelay{socket: socket, session: session})
		_ = outW.Close()
	}()

	t.Cleanup(func() { _ = inW.Close() })

	return &mcpPipe{t: t, in: inW, out: bufio.NewReader(outR)}
}

// ask sends one request and returns its reply.
func (p *mcpPipe) ask(method string, params any) map[string]any {
	p.t.Helper()

	p.id++

	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p.id, "method": method, "params": params})
	if _, err := fmt.Fprintln(p.in, string(raw)); err != nil {
		p.t.Fatal(err)
	}

	line, err := p.out.ReadString('\n')
	if err != nil {
		p.t.Fatalf("read the reply to %s: %v", method, err)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		p.t.Fatalf("reply %q: %v", line, err)
	}

	return m
}

// call runs one tool and returns its reply text and whether the reply is an error.
func (p *mcpPipe) call(name string, args any) (string, bool) {
	p.t.Helper()

	res, _ := p.ask("tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
	if res == nil {
		p.t.Fatalf("%s: no result", name)
	}

	content, _ := res["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	failed, _ := res["isError"].(bool)

	return text, failed
}

func TestEveryToolKindRoundTripsThroughTheEngine(t *testing.T) {
	x := newToolEngine(t)
	p := serveRelay(t, x.socket, "s-1")

	list, _ := p.ask("tools/list", map[string]any{})["result"].(map[string]any)
	if tools, _ := list["tools"].([]any); len(tools) != 11 {
		t.Fatalf("tools/list = %v, want the 11 built-in tools", list)
	}

	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"read", map[string]any{"path": "a.txt"}, "hello"},
		{"search", map[string]any{"pattern": "hel"}, "a.txt"},
		{"write", map[string]any{"path": "b.txt", "content": "new\n"}, "created b.txt, 1 lines"},
		{"edit", map[string]any{"path": "a.txt", "old": "hello", "new": "bye"}, "a.txt"},
		{"run", map[string]any{"class": "build"}, "built"},
		{"diff", map[string]any{}, "diff --git a/a.txt b/a.txt"},
		{"git_read", map[string]any{"command": "log"}, "git log output"},
		{"skill", map[string]any{"name": "standards-go"}, "skill text of standards-go"},
		{"task_done", map[string]any{"task": "T1"}, "exit code 0\nall checks passed"},
		{
			"submit_finding",
			map[string]any{"rule": "G1", "file": "a.txt", "line": 1, "quote": "bye", "note": "n", "confidence": 90},
			"filed G1 at a.txt:1",
		},
		{"lint_plan", map[string]any{"draft": "# x"}, "ok"},
	}
	for _, c := range cases {
		text, failed := p.call(c.tool, c.args)
		if failed || !strings.Contains(text, c.want) {
			t.Errorf("%s = %q (error %v), want it to hold %q", c.tool, text, failed, c.want)
		}
	}

	if got, _ := os.ReadFile(filepath.Join(x.worktree, "a.txt")); string(got) != "bye\n" {
		t.Errorf("a.txt = %q after the edit", got)
	}

	x.mu.Lock()
	defer x.mu.Unlock()

	if len(x.done) != 1 || x.done[0] != "T1" {
		t.Errorf("the engine's task_done callback saw %v, want [T1]", x.done)
	}
	if len(x.found) != 1 || x.found[0].Rule != "G1" {
		t.Errorf("the engine's finding sink saw %v", x.found)
	}
}

func TestRefusalsAreCountedInTheEngineNotTheHelper(t *testing.T) {
	x := newToolEngine(t)
	p := serveRelay(t, x.socket, "s-1")

	for range 2 {
		text, failed := p.call("read", map[string]any{"path": "../outside.txt"})
		if !failed || !strings.Contains(text, "Refused by G2") {
			t.Fatalf("a read outside the worktree = %q (error %v)", text, failed)
		}
	}

	if n := x.bound.Guard().Refusals()["G2"]; n != 2 {
		t.Fatalf("the engine's guard counted %d refusals of G2, want 2", n)
	}
}

func TestRelayReportsAMissingSessionAndAMissingEngine(t *testing.T) {
	x := newToolEngine(t)

	text, failed := serveRelay(t, x.socket, "gone").call("read", map[string]any{"path": "a.txt"})
	if !failed || !strings.Contains(text, "no running session gone") {
		t.Errorf("a call for an ended session = %q (error %v)", text, failed)
	}

	text, failed = serveRelay(t, x.socket+".none", "s-1").call("read", map[string]any{"path": "a.txt"})
	if !failed || !strings.Contains(text, "engine is not running") {
		t.Errorf("a call with no engine = %q (error %v)", text, failed)
	}
}
