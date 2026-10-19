package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// toolsVerb serves one model session's tools on in and out until in ends. The session's inputs come from the arguments
// and the environment of its mcp.json.
func toolsVerb(args []string, in io.Reader, out io.Writer) error {
	cfg, err := task.LoadToolServer(args, os.Getenv)
	if err != nil {
		return fmt.Errorf("read the tool server inputs: %w", err)
	}

	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find the home folder: %w", err)
	}

	home, err := config.DefaultHome()
	if err != nil {
		return err
	}

	// This process holds no sandbox instance, lease or callbacks: run, diff, git_read, skill and task_done report that.
	env := task.Env{Confiner: sandbox.NewManager(buildBackends(home), nil)}

	bound, err := cfg.Bind(userHome, env)
	if err != nil {
		return fmt.Errorf("bind the tools: %w", err)
	}

	defer func() { _ = bound.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return task.ServeMCP(ctx, in, out, bound)
}
