package task

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFakeMCPServer is the outside tool server the tests start as a child process; it only runs when asked to.
func TestFakeMCPServer(t *testing.T) {
	if os.Getenv("KOMODO_FAKE_MCP") != "1" {
		t.Skip("helper process")
	}
	sc := bufio.NewScanner(os.Stdin)
	reply := func(id json.RawMessage, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		_, _ = os.Stdout.Write(append(b, 10))
	}
	for sc.Scan() {
		var req rpcRequest
		if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize":
			reply(req.ID, map[string]any{
				"protocolVersion": mcpVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			reply(req.ID, map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"}},
				{"name": "fail", "description": "fails", "inputSchema": map[string]any{"type": "object"}},
				{"name": "slow", "description": "never answers", "inputSchema": map[string]any{"type": "object"}},
				{"name": "env", "description": "reads TOKEN"},
			}})
		case "tools/call":
			var p struct {
				Name      string
				Arguments map[string]string
			}
			_ = json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "echo":
				reply(req.ID, map[string]any{
					"content": []map[string]string{{"type": "text", "text": "echo:" + p.Arguments["q"]}},
				})
			case "fail":
				reply(req.ID, map[string]any{
					"content": []map[string]string{{"type": "text", "text": "it broke"}},
					"isError": true,
				})
			case "env":
				reply(req.ID, map[string]any{
					"content": []map[string]string{{"type": "text", "text": "token=" + os.Getenv("TOKEN")}},
				})
			case "slow":
				// no reply ever comes
			}
		}
	}
	os.Exit(0)
}

func TestOutsideServerListsAndCallsTools(t *testing.T) {
	conf := &fakeConfiner{dir: t.TempDir()}
	o := startFake(t, conf, fakeServer())
	defs, err := o.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if !json.Valid(d.InputSchema) {
			t.Errorf("%s has a bad schema %s", d.Name, d.InputSchema)
		}
	}
	if strings.Join(names, ",") != "fake.echo,fake.fail,fake.slow,fake.env" {
		t.Fatalf("tools = %v", names)
	}
	if text, out := o.Call(context.Background(), "echo", json.RawMessage(`{"q":"hi"}`)); text != "echo:hi" || out != "ok" {
		t.Fatalf("echo = %q %q", text, out)
	}
	if text, out := o.Call(context.Background(), "fail", nil); text != "error: it broke" || out != "error" {
		t.Fatalf("fail = %q %q", text, out)
	}
	if text, _ := o.Call(context.Background(), "env", nil); text != "token=t0k" {
		t.Fatalf("a named variable must reach the server: %q", text)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if text, out := o.Call(ctx, "slow", nil); out != "timeout" || !strings.HasPrefix(text, "error:") {
		t.Fatalf("slow = %q %q", text, out)
	}
	if text, out := o.Call(context.Background(), "echo", json.RawMessage(`{"q":"after"}`)); text != "echo:after" ||
		out != "ok" {
		t.Fatalf("a timed-out call must not block the next: %q %q", text, out)
	}
}

func TestOutsideServerWithHostsKeepsNoNetworkAndSaysSo(t *testing.T) {
	s := fakeServer()
	s.Hosts = []string{"api.example.com"}
	o := startFake(t, &fakeConfiner{dir: t.TempDir()}, s)
	if w := strings.Join(o.Warnings(), "\n"); !strings.Contains(w, "api.example.com") {
		t.Fatalf("warnings = %v", w)
	}
}

func TestOutsideServerNotNamedIsNeverStarted(t *testing.T) {
	conf := &fakeConfiner{dir: t.TempDir(), env: []string{"KOMODO_FAKE_MCP=1"}}
	conns, err := StartAllowed(
		context.Background(),
		conf,
		"i1",
		Permissions{Servers: []string{"other"}},
		[]OutsideServer{fakeServer()},
		func(string) string { return "" },
	)
	if err != nil || len(conns) != 0 || conf.started != 0 {
		t.Fatalf("conns %d started %d err %v", len(conns), conf.started, err)
	}
	conns, err = StartAllowed(
		context.Background(),
		conf,
		"i1",
		Permissions{Servers: []string{"fake"}},
		[]OutsideServer{fakeServer()},
		func(string) string { return "" },
	)
	if err != nil || len(conns) != 1 || conf.started != 1 {
		t.Fatalf("conns %d started %d err %v", len(conns), conf.started, err)
	}
	_ = conns[0].Close()
}

func TestOutsideToolsReachTheModelThroughTheGuard(t *testing.T) {
	r := newRig(t, nil)
	conf := &fakeConfiner{dir: t.TempDir()}
	o := startFake(t, conf, fakeServer())
	reg := NewRegistry(r.env)
	if err := reg.AddOutsideConn(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	perms := Permissions{Tools: []string{"read", "fake.echo", "fake.fail"}, Servers: []string{"fake"}}
	b, err := reg.Bind(perms.Tools, newTestGuard(t, r.f, "reviewer", perms, nil, 3))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := b.Dispatch(ctx, "fake.echo", json.RawMessage(`{"q":"x"}`)); got.Text != "echo:x" {
		t.Fatalf("allowed call = %q", got.Text)
	}
	if got := b.Dispatch(ctx, "fake.fail", json.RawMessage(`{}`)); !strings.HasPrefix(got.Text, "error:") {
		t.Fatalf("failing call = %q", got.Text)
	}
	if got := b.Dispatch(ctx, "fake.slow", json.RawMessage(`{}`)); !strings.Contains(got.Text, "G1") {
		t.Fatalf("a tool not in the permissions = %q", got.Text)
	}
	var outcomes []string
	for _, f := range r.facts {
		outcomes = append(outcomes, f.Subtype)
	}
	if strings.Join(outcomes, ",") != "tool.outside,tool.outside,tool.denied" {
		t.Fatalf("one fact per call expected, got %v", outcomes)
	}
}

// fakeServer is the entry that starts the helper process.
func fakeServer() OutsideServer {
	return OutsideServer{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestFakeMCPServer$"},
		Env:     []string{"TOKEN"},
	}
}

// startFake starts the helper through a fake confiner.
func startFake(t *testing.T, conf *fakeConfiner, s OutsideServer) *OutsideConn {
	t.Helper()
	conf.env = []string{"KOMODO_FAKE_MCP=1"}
	o, err := StartOutside(context.Background(), conf, "i1", s, func(n string) string {
		if n == "TOKEN" {
			return "t0k"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o
}
