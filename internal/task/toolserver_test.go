package task

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// mcpClient talks to ServeMCP over pipes.
type mcpClient struct {
	t    *testing.T
	in   io.WriteCloser
	out  *bufio.Reader
	done chan error
}

// startMCP serves the bound tools on pipes.
func startMCP(t *testing.T, b *Bound) *mcpClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &mcpClient{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() {
		err := ServeMCP(context.Background(), inR, outW, b)
		_ = outW.Close()
		c.done <- err
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

// send writes one line.
func (c *mcpClient) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

// recv reads one reply.
func (c *mcpClient) recv() map[string]any {
	c.t.Helper()
	line, err := c.out.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read reply: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		c.t.Fatalf("bad reply %q: %v", line, err)
	}
	return m
}

func TestToolServerSpeaksMCP(t *testing.T) {
	r := newRig(t, nil)
	c := startMCP(t, r.b)

	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"claude"}}}`)
	init := c.recv()["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize = %v", init)
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if m := c.recv(); m["id"].(float64) != 2 {
		t.Fatalf("a notification must get no reply, but ping answered %v", m)
	}

	c.send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	tools := c.recv()["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(allTools) {
		t.Fatalf("tools/list = %d tools", len(tools))
	}
	if first := tools[0].(map[string]any); first["name"] != "read" || first["inputSchema"] == nil || first["description"] == "" {
		t.Fatalf("first tool = %v", first)
	}

	c.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"write","arguments":{"path":"made/x.go","content":"a\n"}}}`)
	res := c.recv()["result"].(map[string]any)
	if res["isError"] != false || res["content"].([]any)[0].(map[string]any)["text"] != "created made/x.go, 1 lines" {
		t.Fatalf("write call = %v", res)
	}

	c.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read","arguments":{"path":"../outside/secret.txt"}}}`)
	res = c.recv()["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "G2") {
		t.Fatalf("refused call = %v", res)
	}

	c.send(`{"jsonrpc":"2.0","id":6,"method":"tools/nope"}`)
	if e := c.recv()["error"].(map[string]any); e["code"].(float64) != -32601 {
		t.Fatalf("unknown method = %v", e)
	}
	c.send(`not json`)
	if e := c.recv()["error"].(map[string]any); e["code"].(float64) != -32700 {
		t.Fatalf("parse error = %v", e)
	}
	c.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{}}`)
	if e := c.recv()["error"].(map[string]any); e["code"].(float64) != -32602 {
		t.Fatalf("a call without a name = %v", e)
	}

	_ = c.in.Close()
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop when its input ended")
	}
}

func TestToolServerRunsCallsInParallel(t *testing.T) {
	r := newRig(t, func(e *Env) { e.Commands["build"] = []string{"sh", "-c", "sleep 0.4; echo done"} })
	c := startMCP(t, r.b)
	t0 := time.Now()
	for i := 1; i <= 4; i++ {
		c.send(`{"jsonrpc":"2.0","id":` + string(rune('0'+i)) + `,"method":"tools/call","params":{"name":"run","arguments":{"class":"build"}}}`)
	}
	seen := map[float64]bool{}
	for range 4 {
		m := c.recv()
		seen[m["id"].(float64)] = true
	}
	if len(seen) != 4 || time.Since(t0) > 1500*time.Millisecond {
		t.Fatalf("4 sleeping calls took %s with ids %v", time.Since(t0), seen)
	}
}

func TestToolServerInputsRoundTripThroughMCPConfig(t *testing.T) {
	want := ToolServerConfig{
		Agent: "builder", Session: "s-1", Worktree: "/wt", Dir: "/sess", Files: []string{"a/**", "b.go"},
		Permissions: builderPerms(), Commands: map[string][]string{"test": {"go", "test", "./..."}}, Format: []string{"gofmt", "-w"}, RefusalLimit: 3,
		Servers: []OutsideServer{{Name: "docs", Command: "docs-server", Args: []string{"--stdio"}, Env: []string{"TOKEN"}, Hosts: []string{"example.com"}}},
	}
	raw, err := MCPConfig("/bin/komodo", want)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.MCPServers) != 1 {
		t.Fatalf("mcp.json = %s (%v)", raw, err)
	}
	e := doc.MCPServers["komodo"]
	if e.Command != "/bin/komodo" || strings.Join(e.Args[:2], " ") != "internal tools" {
		t.Fatalf("entry = %+v", e)
	}
	got, err := LoadToolServer(e.Args[2:], func(n string) string { return e.Env[n] })
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v\nerr %v", got, want, err)
	}
	if _, err := LoadToolServer(nil, func(string) string { return "" }); err == nil {
		t.Error("a start with no agent and session must fail")
	}
}

func TestToolServerConfigBindsTheGuard(t *testing.T) {
	f := newFixture(t)
	cfg := ToolServerConfig{Agent: "builder", Session: "s", Worktree: f.Root, Dir: t.TempDir(), Files: []string{"internal/lease/**"},
		Permissions: builderPerms(), Commands: map[string][]string{}, RefusalLimit: 3}
	b, err := cfg.Bind(f.Home, Env{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := b.Dispatch(ctx, "write", json.RawMessage(`{"path":"cmd/x.go","content":"x"}`)); !strings.Contains(got.Text, "B1") {
		t.Fatalf("write outside the files = %q", got.Text)
	}
	if got := b.Dispatch(ctx, "write", json.RawMessage(`{"path":"internal/lease/x.go","content":"x"}`)); !strings.HasPrefix(got.Text, "created") {
		t.Fatalf("write inside the files = %q", got.Text)
	}
	cfg.Permissions.Tools = append(cfg.Permissions.Tools, "teleport")
	if _, err := cfg.Bind(f.Home, Env{}); err == nil {
		t.Error("a permitted tool that is not registered must fail the bind")
	}
}
