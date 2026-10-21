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

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
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
	logRun(tc.env, a.Class, argv, len(a.Args) == 0)
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

// logRun tells the session's role which command the run tool started: the argument vector as text, and the bare class name
// when the model gave no arguments.
func logRun(e *Env, class string, argv []string, bare bool) {
	if e.OnRun == nil {
		return
	}

	e.OnRun(strings.Join(argv, " "))

	if bare {
		e.OnRun(class)
	}
}

// toolRunCheck runs the command of one acceptance item as written, in Quality Assurance's sandbox, and reports its exit
// status and output; the model never supplies the command.
func toolRunCheck(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a checkArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if tc.env.Check == nil {
		return errReply("run_check is not available in this session")
	}

	ids := make([]string, len(tc.env.Items))
	for i, it := range tc.env.Items {
		ids[i] = it.ID
		if it.ID != a.Item {
			continue
		}
		if strings.TrimSpace(it.Cmd) == "" {
			return errReply("the item %s has no command; judge it from the diff and the code", it.ID)
		}

		return checkReply(tc.env.Check(ctx, it.Cmd))
	}

	return errReply(
		"the item %q is not an acceptance item of this group; the items are: %s",
		a.Item,
		strings.Join(ids, ", "),
	)
}

// checkReply words the result of an item's command: the output alone on success, else the exit status and the output.
func checkReply(exit int, out string, err error) Reply {
	switch {
	case errors.Is(err, agent.ErrOutputOverLimit):
		return Reply{Text: fmt.Sprintf("exit status %d\n%s\n%v", exit, out, err)}
	case err != nil:
		return errReply("%v", err)
	case exit != 0:
		return Reply{Text: fmt.Sprintf("exit status %d\n%s", exit, out)}
	}

	return Reply{Text: out}
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
