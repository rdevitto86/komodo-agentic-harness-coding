package cli

import "io"

// ToolsMain runs the tool server of one model session on in and out until in ends; the binary's main sets it, because the
// tool server's parts sit outside this package's imports.
var ToolsMain func(args []string, in io.Reader, out io.Writer) error

// handleTools is the hidden verb that serves one session's tools over standard input and output. Standard output belongs
// to the protocol, so the reply prints nothing there.
func handleTools(c *Call) Result {
	if ToolsMain == nil {
		return Result{Outcome: OutcomeInternal, Silent: true, Stderr: "komodo tools: the tool server is not built yet"}
	}

	args := []string{"--agent", flagString(c, "agent"), "--session", flagString(c, "session")}
	if err := ToolsMain(args, c.cli.in, c.cli.out); err != nil {
		return Result{Outcome: OutcomeInternal, Silent: true, Stderr: "komodo tools: " + err.Error()}
	}

	return Result{Silent: true}
}
