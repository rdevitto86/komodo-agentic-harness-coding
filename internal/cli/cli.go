// Package cli turns a command line into one call into a domain package.
package cli

import (
	"fmt"
	"io"
	"os"
)

// Exit codes every verb returns to the shell.
const (
	ExitOK       = 0 // the verb succeeded
	ExitRefused  = 1 // refused on purpose; error.fix says what to do
	ExitUsage    = 2 // unknown verb or flag, or a bad value
	ExitBlocked  = 3 // the verb reports a blocked state
	ExitInternal = 4 // komodo itself failed
)

// verb is one entry in the verb table.
type verb struct {
	run func(args []string, out io.Writer) int // runs the verb and returns its exit code
}

// verbs maps each verb name to its handler; verbs are added as their subsystems land.
var verbs = map[string]verb{}

// Run dispatches args[1] to its verb and returns the process exit code.
// A missing or unknown verb prints usage to stderr and returns ExitUsage.
func Run(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: komodo <verb> [flags]")
		return ExitUsage
	}
	v, ok := verbs[args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown verb %q\nusage: komodo <verb> [flags]\n", args[1])
		return ExitUsage
	}
	return v.run(args[2:], os.Stdout)
}
