package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// How long an outside server gets to answer.
const (
	outsideInit = 30 * time.Second // initialize and tools/list
	outsideCall = runTimeout       // one tool call
)

// OutsideConn is one running outside tool server, spoken to over its standard input and output.
type OutsideConn struct {
	name     string
	cmd      *exec.Cmd
	in       io.WriteCloser
	cancel   context.CancelFunc
	warnings []string

	wmu     sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[int]chan rpcResponse
	gone    chan struct{} // closed when the server's output ends
}

// rpcResponse is one reply from an outside server.
type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// StartOutside starts one outside server inside the sandbox instance of the agent's role, without a shell and without network.
// The variables named in its entry are read from getenv; it dies with ctx.
func StartOutside(ctx context.Context, c Confiner, instance string, s OutsideServer, getenv func(string) string) (*OutsideConn, error) {
	if c == nil || instance == "" || s.Command == "" || s.Name == "" {
		return nil, errors.New("an outside server needs a name, a command and a sandbox instance")
	}
	var env []string
	for _, n := range s.Env {
		if v := getenv(n); v != "" {
			env = append(env, n+"="+v)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cmd, err := c.Command(cctx, instance, sandbox.Command{Argv: append([]string{s.Command}, s.Args...), Env: env})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start the outside server %s: %w", s.Name, err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open the input of %s: %w", s.Name, err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open the output of %s: %w", s.Name, err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start the outside server %s: %w", s.Name, err)
	}
	o := &OutsideConn{name: s.Name, cmd: cmd, in: in, cancel: cancel, pending: map[int]chan rpcResponse{}, gone: make(chan struct{})}
	if len(s.Hosts) > 0 {
		o.warnings = append(o.warnings, fmt.Sprintf("outside server %s lists hosts %s, but no mechanism limits a server to hosts, so it keeps no network", s.Name, strings.Join(s.Hosts, ", ")))
	}
	go func() {
		defer guardian.Recover(taskSource, nil)
		o.read(out)
	}()
	hctx, hcancel := context.WithTimeout(ctx, outsideInit)
	defer hcancel()
	if _, err := o.rpc(hctx, "initialize", map[string]any{"protocolVersion": mcpVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "komodo", "version": "1"}}); err != nil {
		_ = o.Close()
		return nil, fmt.Errorf("initialize the outside server %s: %w", s.Name, err)
	}
	if err := o.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		_ = o.Close()
		return nil, fmt.Errorf("start the outside server %s: %w", s.Name, err)
	}
	return o, nil
}

// Warnings lists what the server's entry asked for and the sandbox could not give.
func (o *OutsideConn) Warnings() []string { return append([]string{}, o.warnings...) }

// read routes each reply to the call waiting for it, and answers a request from the server with "method not found".
func (o *OutsideConn) read(out io.Reader) {
	defer close(o.gone)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64<<10), maxRPCLine)
	for sc.Scan() {
		var m struct {
			rpcResponse
			Method string `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method != "" {
			if len(m.ID) > 0 {
				_ = o.send(rpcReply{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
			}
			continue
		}
		var id int
		if json.Unmarshal(m.ID, &id) != nil {
			continue
		}
		o.mu.Lock()
		ch := o.pending[id]
		delete(o.pending, id)
		o.mu.Unlock()
		if ch != nil {
			ch <- m.rpcResponse
		}
	}
}

// send writes one JSON line to the server.
func (o *OutsideConn) send(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	o.wmu.Lock()
	defer o.wmu.Unlock()
	_, err = o.in.Write(append(raw, '\n'))
	return err
}

// rpc sends one request and waits for its reply, for ctx to end, or for the server to exit.
func (o *OutsideConn) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	o.mu.Lock()
	o.next++
	id := o.next
	ch := make(chan rpcResponse, 1)
	o.pending[id] = ch
	o.mu.Unlock()
	forget := func() { o.mu.Lock(); delete(o.pending, id); o.mu.Unlock() }
	if err := o.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		forget()
		return nil, fmt.Errorf("send to the server: %w", err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("%s", r.Error.Message)
		}
		return r.Result, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case <-o.gone:
		forget()
		return nil, errors.New("the server exited")
	}
}

// Tools lists the server's tools as definitions named "<server>.<tool>".
func (o *OutsideConn) Tools(ctx context.Context) ([]llm.ToolDef, error) {
	ctx, cancel := context.WithTimeout(ctx, outsideInit)
	defer cancel()
	raw, err := o.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list the tools of %s: %w", o.name, err)
	}
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("read the tools of %s: %w", o.name, err)
	}
	out := make([]llm.ToolDef, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, llm.ToolDef{Name: o.name + "." + t.Name, Description: t.Description, InputSchema: schema})
	}
	return out, nil
}

// Call runs one tool of the server and returns its text and the outcome: ok, error or timeout.
func (o *OutsideConn) Call(ctx context.Context, tool string, args json.RawMessage) (string, string) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	cctx, cancel := context.WithTimeout(ctx, outsideCall)
	defer cancel()
	raw, err := o.rpc(cctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "error: timed out waiting for " + o.name + "." + tool, "timeout"
	case err != nil:
		return "error: " + err.Error(), "error"
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "error: the server's reply cannot be read", "error"
	}
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		} else {
			parts = append(parts, "[non-text content omitted]")
		}
	}
	text := strings.Join(parts, "\n")
	if res.IsError {
		return "error: " + text, "error"
	}
	return text, "ok"
}

// Close ends the server: it closes its input, kills its process tree and waits for it.
func (o *OutsideConn) Close() error {
	_ = o.in.Close()
	o.cancel()
	err := sandbox.Wait(o.cmd)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil
	}
	return err
}

// AddOutsideConn registers every tool of a running server as "<server>.<tool>".
func (r *Registry) AddOutsideConn(ctx context.Context, o *OutsideConn) error {
	defs, err := o.Tools(ctx)
	if err != nil {
		return err
	}
	for _, d := range defs {
		tool := strings.TrimPrefix(d.Name, o.name+".")
		r.AddOutside(d, func(ctx context.Context, args json.RawMessage) (string, string) { return o.Call(ctx, tool, args) })
	}
	return nil
}

// StartAllowed starts the servers an agent's permissions name and no others; a server nobody named is never started.
// On a failure the servers already started are closed.
func StartAllowed(ctx context.Context, c Confiner, instance string, perm Permissions, servers []OutsideServer, getenv func(string) string) ([]*OutsideConn, error) {
	var started []*OutsideConn
	for _, s := range servers {
		if !slices.Contains(perm.Servers, s.Name) {
			continue
		}
		o, err := StartOutside(ctx, c, instance, s, getenv)
		if err != nil {
			for _, x := range started {
				_ = x.Close()
			}
			return nil, err
		}
		started = append(started, o)
	}
	return started, nil
}
