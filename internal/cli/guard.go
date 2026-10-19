package cli

import (
	"fmt"
	"os"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// exitBlock is the exit code Claude Code reads from a PreToolUse hook as "refuse this tool call".
const exitBlock = Outcome(2)

// guardCheck decides one hook call; a test replaces it to force a crash.
var guardCheck = task.CheckHook

// handleGuard is the hidden verb behind the repository's PreToolUse hook. It reads Claude Code's JSON on standard input,
// prints {} and exits 0 to let the call through, and exits 2 with the reason on standard error for every refusal and error.
func handleGuard(c *Call) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = blockCall(fmt.Sprintf("the guard crashed: %v", r))
		}
	}()

	in, err := task.ReadHookInput(c.cli.in)
	if err != nil {
		return blockCall(err.Error())
	}

	cwd := in.Cwd
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return blockCall("find the session folder: " + err.Error())
		}
	}

	root := cwd
	if r, ok := findRepoRoot(cwd); ok {
		root = r
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return blockCall("find the home folder: " + err.Error())
	}

	v, err := guardCheck(home, root, in)
	switch {
	case err != nil:
		return blockCall(err.Error())
	case !v.Allowed:
		return blockCall(v.Message + " " + v.Alternative)
	}

	return Result{Raw: "{}"}
}

// blockCall builds the reply that makes Claude Code refuse the tool call and show the reason to the model.
func blockCall(reason string) Result {
	return Result{Outcome: exitBlock, Silent: true, Stderr: "komodo guard: " + reason}
}
