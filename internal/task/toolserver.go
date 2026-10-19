package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// ToolServerConfig is what the tool server process of one session starts from: who it serves and where the engine listens.
type ToolServerConfig struct {
	Agent   string
	Session string
	Socket  string // the engine's control socket; empty means the one under the Komodo home folder
}

// ToolBackend is where the tool server sends what the program asks; the engine's tools answer it through a relay.
type ToolBackend interface {
	// List returns the definitions of the tools the session may see.
	List(ctx context.Context) ([]llm.ToolDef, error)
	// Call runs one tool call and returns the text the model receives.
	Call(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// rpcRequest is one JSON-RPC message from the client.
type rpcRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// rpcError is the error member of a JSON-RPC reply.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcReply is one JSON-RPC reply.
type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpVersion is the protocol version the tool server answers with when the client names none.
const mcpVersion = "2024-11-05"

// maxRPCLine is the longest request line the tool server reads.
const maxRPCLine = 16 << 20

// EnvSocket is the variable that names the engine's control socket for the tool server process.
const EnvSocket = "KOMODO_TOOLS_SOCKET"

// toolServerVerb is the hidden verb of the same binary that runs the tool server.
var toolServerVerb = []string{"internal", "tools"}

// MCPConfig renders the mcp.json of a session: the one tool server entry, with the session and the socket it relays to.
func MCPConfig(exe string, c ToolServerConfig) ([]byte, error) {
	entry := map[string]any{
		"command": exe,
		"args": append(
			append([]string{}, toolServerVerb...),
			"--agent",
			c.Agent,
			"--session",
			c.Session,
		),
		"env": map[string]string{EnvSocket: c.Socket},
	}

	return json.MarshalIndent(
		map[string]any{"mcpServers": map[string]any{"komodo": entry}},
		"",
		"  ",
	)
}

// LoadToolServer reads the inputs of the tool server process from its arguments (after the verb) and environment.
func LoadToolServer(args []string, getenv func(string) string) (ToolServerConfig, error) {
	var c ToolServerConfig
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--agent":
			c.Agent = args[i+1]
		case "--session":
			c.Session = args[i+1]
		default:
			return c, fmt.Errorf("unknown argument %q", args[i])
		}
	}
	if c.Agent == "" || c.Session == "" {
		return c, errors.New("the tool server needs --agent and --session")
	}

	c.Socket = getenv(EnvSocket)
	return c, nil
}

// ServeMCP answers the Model Context Protocol on in and out until in ends: initialize, ping, tools/list and tools/call. Tool
// calls run in their own goroutines so one long command does not
// hold up the next call.
func ServeMCP(ctx context.Context, in io.Reader, out io.Writer, b ToolBackend) error {
	var (
		wmu sync.Mutex
		wg  sync.WaitGroup
	)

	send := func(r rpcReply) {
		r.JSONRPC = "2.0"

		raw, err := json.Marshal(r)
		if err != nil {
			return
		}

		wmu.Lock()
		defer wmu.Unlock()

		_, _ = out.Write(append(raw, '\n'))
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxRPCLine)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			send(
				rpcReply{
					ID:    json.RawMessage("null"),
					Error: &rpcError{Code: -32700, Message: "parse error"},
				},
			)
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		if req.Method == "tools/call" {
			wg.Add(1)
			go func() {
				defer guardian.Recover(taskSource, nil)
				defer wg.Done()
				send(callTool(ctx, b, req))
			}()
			continue
		}

		send(answer(ctx, b, req))
	}

	wg.Wait()
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read the tool server input: %w", err)
	}

	return nil
}

// answer replies to every method except tools/call.
func answer(ctx context.Context, b ToolBackend, req rpcRequest) rpcReply {
	r := rpcReply{ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		r.Result = map[string]any{
			"protocolVersion": cmpOr(p.ProtocolVersion, mcpVersion),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "komodo", "version": "1"},
		}
	case "ping":
		r.Result = map[string]any{}
	case "tools/list":
		defs, err := b.List(ctx)
		if err != nil {
			r.Error = &rpcError{Code: -32603, Message: "list the tools: " + err.Error()}
			break
		}

		tools := []map[string]any{}
		for _, d := range defs {
			tools = append(
				tools,
				map[string]any{
					"name":        d.Name,
					"description": d.Description,
					"inputSchema": d.InputSchema,
				},
			)
		}

		r.Result = map[string]any{"tools": tools}
	default:
		r.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	return r
}

// callTool forwards one tools/call to the backend, where the guard sees it first.
func callTool(ctx context.Context, b ToolBackend, req rpcRequest) rpcReply {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		return rpcReply{
			ID:    req.ID,
			Error: &rpcError{Code: -32602, Message: "invalid params: a tool call needs a name"},
		}
	}

	text, err := b.Call(ctx, p.Name, p.Arguments)
	if err != nil {
		text = "error: " + err.Error()
	}

	failed := strings.HasPrefix(text, "error:") ||
		strings.HasPrefix(text, "Refused by ")

	return rpcReply{ID: req.ID, Result: map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": failed,
	}}
}
