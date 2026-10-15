package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// mcpVersion is the protocol version the tool server answers with when the client names none.
const mcpVersion = "2024-11-05"

// maxRPCLine is the longest request line the tool server reads.
const maxRPCLine = 16 << 20

// The variables the tool server process reads its inputs from; the files and permissions travel as JSON text.
const (
	EnvWorktree     = "KOMODO_TOOLS_WORKTREE"
	EnvSessionDir   = "KOMODO_TOOLS_SESSION_DIR"
	EnvFiles        = "KOMODO_TOOLS_FILES"
	EnvPermissions  = "KOMODO_TOOLS_PERMISSIONS"
	EnvCommands     = "KOMODO_TOOLS_COMMANDS"
	EnvFormat       = "KOMODO_TOOLS_FORMAT"
	EnvRefusalLimit = "KOMODO_TOOLS_REFUSAL_LIMIT"
)

// ToolServerConfig is what the tool server process of one session starts from.
type ToolServerConfig struct {
	Agent        string
	Session      string
	Worktree     string
	Dir          string              // the session folder
	Files        []string            // the group's effective file list
	Permissions  Permissions         // tools, run classes, guard rows and outside servers
	Commands     map[string][]string // command class to argv
	Format       []string            // format command argv, or none
	RefusalLimit int
}

// toolServerVerb is the hidden verb of the same binary that runs the tool server.
var toolServerVerb = []string{"internal", "tools"}

// MCPConfig renders the mcp.json of a session: the one tool server entry, with its inputs in arguments and environment.
func MCPConfig(exe string, c ToolServerConfig) ([]byte, error) {
	files, err := json.Marshal(c.Files)
	if err != nil {
		return nil, fmt.Errorf("encode the file list: %w", err)
	}
	perms, err := json.Marshal(c.Permissions)
	if err != nil {
		return nil, fmt.Errorf("encode the permissions: %w", err)
	}
	cmds, err := json.Marshal(c.Commands)
	if err != nil {
		return nil, fmt.Errorf("encode the commands: %w", err)
	}
	format, err := json.Marshal(c.Format)
	if err != nil {
		return nil, fmt.Errorf("encode the format command: %w", err)
	}
	entry := map[string]any{
		"command": exe,
		"args":    append(append([]string{}, toolServerVerb...), "--agent", c.Agent, "--session", c.Session),
		"env": map[string]string{
			EnvWorktree: c.Worktree, EnvSessionDir: c.Dir, EnvFiles: string(files), EnvPermissions: string(perms),
			EnvCommands: string(cmds), EnvFormat: string(format), EnvRefusalLimit: strconv.Itoa(c.RefusalLimit),
		},
	}
	return json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"komodo": entry}}, "", "  ")
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
	c.Worktree, c.Dir = getenv(EnvWorktree), getenv(EnvSessionDir)
	for name, dst := range map[string]any{EnvFiles: &c.Files, EnvPermissions: &c.Permissions, EnvCommands: &c.Commands, EnvFormat: &c.Format} {
		if v := getenv(name); v != "" {
			if err := json.Unmarshal([]byte(v), dst); err != nil {
				return c, fmt.Errorf("read %s: %w", name, err)
			}
		}
	}
	n, err := strconv.Atoi(getenv(EnvRefusalLimit))
	if err != nil {
		return c, fmt.Errorf("read %s: %w", EnvRefusalLimit, err)
	}
	c.RefusalLimit = n
	return c, nil
}

// Bind compiles the guard for the session, registers the tools over env and binds the permitted ones.
// The caller sets the environment's sandbox, host git and callbacks; the rest comes from the configuration.
func (c ToolServerConfig) Bind(home string, env Env) (*Bound, error) {
	env.Worktree, env.Dir, env.Commands, env.Format = c.Worktree, c.Dir, c.Commands, c.Format
	rows, err := LoadRows(home)
	if err != nil {
		return nil, err
	}
	g, err := NewGuard(rows, Binding{Agent: c.Agent, Permissions: c.Permissions, Worktree: c.Worktree, Files: c.Files}, c.RefusalLimit)
	if err != nil {
		return nil, fmt.Errorf("compile the guard: %w", err)
	}
	return NewRegistry(&env).Bind(c.Permissions.Tools, g)
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

// ServeMCP answers the Model Context Protocol on in and out until in ends: initialize, ping, tools/list and tools/call.
// Tool calls run in their own goroutines so one long command does not hold up the next call.
func ServeMCP(ctx context.Context, in io.Reader, out io.Writer, b *Bound) error {
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
			send(rpcReply{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
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
		send(answer(b, req))
	}
	wg.Wait()
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read the tool server input: %w", err)
	}
	return nil
}

// answer replies to every method except tools/call.
func answer(b *Bound, req rpcRequest) rpcReply {
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
		tools := []map[string]any{}
		for _, n := range b.Advertised() {
			if d, ok := b.Definition(n); ok {
				tools = append(tools, map[string]any{"name": d.Name, "description": d.Description, "inputSchema": d.InputSchema})
			}
		}
		r.Result = map[string]any{"tools": tools}
	default:
		r.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return r
}

// callTool runs one tools/call through the bound tools, so the guard sees it first.
func callTool(ctx context.Context, b *Bound, req rpcRequest) rpcReply {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		return rpcReply{ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params: a tool call needs a name"}}
	}
	reply := b.Dispatch(ctx, p.Name, p.Arguments)
	failed := strings.HasPrefix(reply.Text, "error:") || strings.HasPrefix(reply.Text, "Refused by ")
	return rpcReply{ID: req.ID, Result: map[string]any{
		"content": []map[string]string{{"type": "text", "text": reply.Text}},
		"isError": failed,
	}}
}
