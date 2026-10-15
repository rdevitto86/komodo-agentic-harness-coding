// Command komodo is the harness binary.
package main

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// registerFaults binds the fault event types once per process.
var registerFaults sync.Once

// main runs the command line and exits with the code the CLI returns.
func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// run builds the harness parts, runs one command and returns its exit code.
func run(args []string, stdout, stderr io.Writer) int {
	registerFaults.Do(fault.RegisterEvents)
	deps, err := buildDeps(stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "komodo:", err)
		return cli.ExitInternal
	}
	return cli.New(deps).Run(args)
}

// buildDeps opens the settings and the formatter from the output keys and wires the engine client.
func buildDeps(stdout, stderr io.Writer) (cli.Deps, error) {
	home, err := config.DefaultHome()
	if err != nil {
		return cli.Deps{}, err
	}
	cfg, err := config.New(home, config.Known{}, func(string) bool { return true })
	if err != nil {
		return cli.Deps{}, fmt.Errorf("open settings: %w", err)
	}
	opts := format.Options{Profile: format.ProfileAdhd, Color: format.ColorOff}
	if it, err := cfg.GetConfigItem("output.profile"); err == nil && it.Value == string(format.ProfileStandard) {
		opts.Profile = format.ProfileStandard
	}
	if it, err := cfg.GetConfigItem("output.turn_end_summary"); err == nil {
		opts.TurnEnd, _ = it.Value.(bool)
	}
	if it, err := cfg.GetConfigItem("output.color"); err == nil && colorOn(it.Value, stdout) {
		opts.Color = format.ColorOn
	}
	f, err := format.New(opts)
	if err != nil {
		return cli.Deps{}, fmt.Errorf("load output templates: %w", err)
	}
	return cli.Deps{
		Format: f, Config: cfg, Engine: cli.NewSocketEngine(home),
		Terminal: cli.HasControllingTerminal, Stdout: stdout, Stderr: stderr,
	}, nil
}

// colorOn resolves output.color: on, or auto with a terminal on standard output and NO_COLOR unset.
func colorOn(setting any, stdout io.Writer) bool {
	switch setting {
	case "on":
		return true
	case "auto":
		f, ok := stdout.(*os.File)
		if !ok || os.Getenv("NO_COLOR") != "" {
			return false
		}
		fi, err := f.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
	return false
}
