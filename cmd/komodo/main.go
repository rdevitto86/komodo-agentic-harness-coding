// Command komodo is the harness binary.
package main

import (
	"os"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
)

// main hands the command line to cli.Run and exits with its code.
func main() {
	os.Exit(cli.Run(os.Args))
}
