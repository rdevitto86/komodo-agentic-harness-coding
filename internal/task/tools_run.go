package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// timeoutLine starts the reply of a command the harness killed for running too long.
const timeoutLine = "error: timed out"

// timeout returns the longest a run command may take.
func (e *Env) timeout() time.Duration {
	if e.RunTimeout > 0 {
		return e.RunTimeout
	}
	return runTimeout
}

// toolRun starts the command of a class with the model's arguments as separate arguments, inside the sandbox.
func toolRun(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a RunArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}

	base := tc.env.Commands[a.Class]
	if len(base) == 0 {
		return errReply("the class %q has no command in this repository", a.Class)
	}

	argv := append(append([]string{}, base...), a.Args...)
	res := confinedRun(ctx, tc, argv, runLogPath(tc))
	switch {
	case res.StartErr != nil:
		return errReply("%s", strings.TrimPrefix(res.StartErr.Error(), "error: "))
	case res.TimedOut:
		r := res.Reply
		r.Text = fmt.Sprintf(
			"%s after %s; the command and its processes were killed\n%s",
			timeoutLine,
			tc.env.timeout(),
			r.Text,
		)
		return r
	case res.Exit != 0:
		r := res.Reply
		r.Text = fmt.Sprintf("exit status %d\n%s", res.Exit, r.Text)
		return r
	}

	return res.Reply
}

// confinedRun starts argv in the session's sandbox instance and collects its output; it never runs a command outside the
// sandbox.
func confinedRun(ctx context.Context, tc *toolCall, argv []string, logPath string) runResult {
	env := tc.env
	if env.Confiner == nil || env.Instance == "" {
		return runResult{StartErr: errors.New("error: no sandbox instance is set for this session, so no command can start")}
	}
	if tc.guard != nil {
		if err := env.Confiner.SetMasked(env.Instance, tc.guard.SecretFiles()); err != nil {
			return runResult{StartErr: fmt.Errorf("error: refresh the masked files: %w", err)}
		}
	}

	cctx, cancel := context.WithTimeout(ctx, env.timeout())
	defer cancel()

	cmd, err := env.Confiner.Command(cctx, env.Instance, sandbox.Command{Argv: argv, Timeout: env.timeout()})
	if err != nil {
		return runResult{StartErr: err}
	}

	return runConfined(cctx, cmd, logPath)
}

// runLogPath names the log of the current call, in the session's out folder or in a temporary one.
func runLogPath(tc *toolCall) string {
	dir := tc.env.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "komodo-task-out")
	}
	return filepath.Join(dir, "out", tc.id+".log")
}
