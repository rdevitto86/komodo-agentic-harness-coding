package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// toolsVerb serves one model session's tools on in and out until in ends, forwarding every request to the running engine.
// The session and the engine's socket come from the arguments and the environment of its mcp.json.
func toolsVerb(args []string, in io.Reader, out io.Writer) error {
	cfg, err := task.LoadToolServer(args, os.Getenv)
	if err != nil {
		return fmt.Errorf("read the tool server inputs: %w", err)
	}

	socket := cfg.Socket
	if socket == "" {
		home, err := config.DefaultHome()
		if err != nil {
			return err
		}

		socket = filepath.Join(home, "state", engine.SocketName)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return task.ServeMCP(ctx, in, out, &engineRelay{socket: socket, session: cfg.Session})
}

// engineRelay is the tool server's backend: it holds no state and sends each request to the engine, which owns the
// session's guard, refusal count and task record.
type engineRelay struct {
	socket  string
	session string
	seq     atomic.Int64
}

// List asks the engine for the tools the session may see.
func (r *engineRelay) List(ctx context.Context) ([]llm.ToolDef, error) {
	rep, err := r.ask(ctx, engine.Request{Verb: engine.VerbToolList, Tool: &engine.ToolRequest{Session: r.session}})
	if err != nil {
		return nil, err
	}

	var l engine.ToolList
	if err := json.Unmarshal(rep.Data, &l); err != nil {
		return nil, fmt.Errorf("read the tool list: %w", err)
	}

	return l.Tools, nil
}

// Call sends one tool call to the engine and returns the text the engine's tools answered.
func (r *engineRelay) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	rep, err := r.ask(
		ctx,
		engine.Request{
			Verb: engine.VerbToolCall,
			Tool: &engine.ToolRequest{Session: r.session, Name: name, Args: args},
		},
	)
	if err != nil {
		return "", err
	}

	var a engine.ToolAnswer
	if err := json.Unmarshal(rep.Data, &a); err != nil {
		return "", fmt.Errorf("read the tool answer: %w", err)
	}

	return a.Text, nil
}

// ask sends one request on a connection of its own, so a long call never holds up another, and returns the final reply.
// The connection closes when ctx ends, which cancels the call in the engine.
func (r *engineRelay) ask(ctx context.Context, req engine.Request) (engine.Reply, error) {
	req.ID = r.session + "-" + strconv.FormatInt(r.seq.Add(1), 10)

	c, err := engine.Dial(r.socket)
	if err != nil {
		if errors.Is(err, engine.ErrNoEngine) {
			return engine.Reply{}, errors.New("the engine is not running, so this session's tools are unavailable")
		}

		return engine.Reply{}, fmt.Errorf("reach the engine: %w", err)
	}

	defer context.AfterFunc(ctx, func() { _ = c.Close() })()
	defer func() { _ = c.Close() }()

	if err := c.Send(req); err != nil {
		return engine.Reply{}, fmt.Errorf("send to the engine: %w", err)
	}

	for {
		rep, err := c.Recv()
		if err != nil {
			return engine.Reply{}, fmt.Errorf("read from the engine: %w", err)
		}
		if !rep.Final {
			continue
		}
		if rep.Status != engine.StatusOK {
			return engine.Reply{}, fmt.Errorf("the engine refused the request: %s", rep.Detail)
		}

		return rep, nil
	}
}
