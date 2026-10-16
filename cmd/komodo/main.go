// Command komodo is the harness binary.
package main

import (
	"context"
	"fmt"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// main runs the command line and exits with the code the CLI returns.
func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// run builds the harness parts, runs one command and returns its exit code.
func run(args []string, stdout, stderr io.Writer) int {
	registerAllEvents.Do(registerEvents)
	cli.EngineMain = func(c *cli.Call) error { return engineVerb(c, stderr) }
	deps, err := buildDeps(stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "komodo:", err)
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
	var links cli.LinkProber
	if cat, err := llm.LoadCatalog(); err == nil {
		links = buildLinks(cfg, cat, nil)
	}
	return cli.Deps{
		Format: f, Config: cfg, Engine: cli.NewSocketEngine(home),
		Terminal: cli.HasControllingTerminal, Home: home, Stdout: stdout, Stderr: stderr,
		Host: cli.Host{Links: links, InUse: versionsInUse},
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

// engineVerb runs the engine in the foreground until its exit rule holds or a signal arrives.
func engineVerb(_ *cli.Call, stderr io.Writer) error {
	home, err := config.DefaultHome()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(home, "state", "engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			defer func() { _ = f.Close() }()
			stderr = io.MultiWriter(stderr, f)
		}
	}
	return runEngine(ctx, home, stderr)
}

// versionsInUse names the binary versions that unfinished groups of the current repository pin; the records pin none, so it is the running version.
func versionsInUse() ([]string, error) {
	common, err := gitOut("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, nil
	}
	st, err := state.New(filepath.Join(common, "komodo"), "cli")
	if err != nil {
		return nil, err
	}
	recs, err := st.List()
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.State.Stage != state.StageDone {
			return []string{version}, nil
		}
	}
	return nil, nil
}
