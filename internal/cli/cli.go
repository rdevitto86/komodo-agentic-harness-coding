// Package cli turns a command line into one call into a domain package and prints exactly one reply.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// ExitInternal is the exit code of a harness failure before any command runs.
const ExitInternal = int(OutcomeInternal)

// Envelope is the one JSON reply every command prints under --json.
type Envelope struct {
	V       int        `json:"v"`
	OK      bool       `json:"ok"`
	Verdict string     `json:"verdict"`
	Report  string     `json:"report"`
	Data    any        `json:"data"` // an empty object, never nil, when the command returns none
	Error   *ErrorBody `json:"error,omitempty"`
}

// ErrorBody explains a refusal or failure; a caller branches on Code.
type ErrorBody struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
}

// Outcome classifies a finished command; its value is the process exit code.
type Outcome int

const (
	OutcomeOK       Outcome = 0 // the command did what was asked
	OutcomeRefused  Outcome = 1 // the harness declined on purpose
	OutcomeUsage    Outcome = 2 // the command line was wrong
	OutcomeHeld     Outcome = 3 // the command ran and reports a group or a check that needs a person
	OutcomeInternal Outcome = 4 // the harness itself failed
)

// Result is what a handler returns; render turns Shape and Facts into the report and the verdict.
type Result struct {
	Outcome Outcome
	Shape   string // the reply shape the Output Formatter prints, such as "reply.run"
	Code    string // reason code from the closed code list; empty on plain success
	Facts   any    // the values the shape's template prints
	Data    any    // the command's typed result, placed in Envelope.Data
	Reason  string // set when Outcome is not OutcomeOK
	Fix     string // the command or change that resolves it
	Raw     string // text printed as is, for help; the formatter is skipped
}

// Tier says who may call a command.
type Tier int

const (
	TierShared   Tier = iota // a person or the orchestrator
	TierTerminal             // a person at a controlling terminal only
)

// FlagSpec describes one command flag for parsing, help and the drift test.
type FlagSpec struct {
	Name    string // long name without dashes, such as "dry-run"
	Short   string // one-letter form; empty when none
	Kind    string // bool, string, int, duration or time (an RFC 3339 timestamp)
	Default string // text form of the default; empty when none
	Help    string // one line shown by help
}

// Command is one row of the command table.
type Command struct {
	Name          string // "run", "config get"
	Summary       string // one line shown by help
	Tier          Tier
	Flags         []FlagSpec         // command-specific flags; global flags are added by parse
	NeedsTerminal func(c *Call) bool // nil when the command never needs a terminal
	NeedsRepo     func(c *Call) bool // nil when the command works outside a repository
	NeedsEngine   bool               // connects to the engine, starting it when none answers
	PinExempt     bool               // runs even when the repository pin is above this binary
	TamperPolicy  TamperPolicy       // what the command does when the settings guard has tripped
	Run           Handler
}

// TamperPolicy says what a command does when the Configuration Manager reports a hand edit.
type TamperPolicy int

const (
	TamperRefuse TamperPolicy = iota // refuse with config_tampered
	TamperWarn                       // run, and print one warning line
)

// GlobalFlags holds the flags every command accepts.
type GlobalFlags struct {
	JSON    bool
	Dir     string // -C: act on the repository at this path
	Input   string // --input: file path, or "-" for standard input
	Quiet   bool
	Verbose bool
	NoColor bool
	DryRun  bool
	List    bool // --list
	All     bool // --all
	Version bool // --version
}

// RepoRef names the repository a command acts on.
type RepoRef struct {
	ID   string // normalized origin URL, or the git common dir path with no remote
	Root string
}

// Call is everything a handler needs, filled by the first three steps.
type Call struct {
	Command     *Command
	Args        []string      // positional arguments after flag parsing
	Flags       *flag.FlagSet // the command's parsed flags
	Global      GlobalFlags
	Repo        RepoRef // zero value when the command needs no repository
	HasTerminal bool
	Stdout      io.Writer
	CommonDir   string   // the repository's git common directory; empty outside a repository
	Groups      []string // the group labels a run label expands to
	cli         *CLI
}

// Handler is one command's thin call into the part that owns the work.
type Handler func(c *Call) Result

// Deps are the parts the CLI calls; Stdout, Stderr and Stdin default to the process streams.
type Deps struct {
	Format   *format.Formatter // FormatJSON()
	Config   *config.Manager   // GetConfigItem(), SetConfigItem(), Verify()
	Engine   Engine            // the control socket client
	Terminal func() bool       // true when a controlling terminal is attached
	Stdout   io.Writer
	Stderr   io.Writer
	Stdin    io.Reader
}

// Engine is the CLI's client of the engine's control socket.
type Engine interface {
	// Ensure connects, starting the engine first when none answers.
	Ensure(ctx context.Context) error
	// Send writes one request and returns the replies until the one that ends it.
	Send(ctx context.Context, r engine.Request) (<-chan engine.Reply, error)
}

// CLI is the command-line front door; it holds the parts in Deps.
type CLI struct {
	deps    Deps
	fault   *fault.Handler
	out     io.Writer
	errOut  io.Writer
	in      io.Reader
	json    bool // print the envelope
	noColor bool
}

// New returns a CLI that calls the parts in d and prints faults through itself, with no bus.
func New(d Deps) *CLI {
	c := &CLI{deps: d, out: d.Stdout, errOut: d.Stderr, in: d.Stdin}
	if c.out == nil {
		c.out = os.Stdout
	}
	if c.errOut == nil {
		c.errOut = os.Stderr
	}
	if c.in == nil {
		c.in = os.Stdin
	}
	c.fault = fault.New(nil, c)
	return c
}

// Run parses args, runs one command and returns the process exit code.
func (c *CLI) Run(args []string) int {
	hasTerm := c.deps.Terminal != nil && c.deps.Terminal()
	c.json = !hasTerm || hasToken(args, "--json", "-json")
	c.noColor = os.Getenv("NO_COLOR") != "" || hasToken(args, "--no-color", "-no-color")
	src := fault.Source{Component: "cli"}
	code := int(OutcomeInternal)
	err := c.fault.Guard(src, func() error {
		code = c.run(args, hasTerm)
		return nil
	})
	if err != nil {
		var f fault.Fault
		if errors.As(err, &f) {
			return c.fault.Final(context.Background(), src, f)
		}
	}
	return code
}

// run takes the five steps for one command line.
func (c *CLI) run(args []string, hasTerm bool) int {
	if len(args) > 0 {
		args = args[1:]
	}
	call, res := c.parse(args, hasTerm)
	if res == nil {
		res = c.validate(call)
	}
	if res == nil {
		res = c.resolve(call)
	}
	if res == nil && call.Global.List && call.Command.Name != "config get" {
		r := notBuilt("--list for komodo " + call.Command.Name + ", which has no list reply shape in the Output Formatter,")
		res = &r
	}
	if res == nil {
		r := call.Command.Run(call)
		res = &r
	}
	return c.render(*res)
}

// validate runs the pin check, the value checks, the terminal check and the settings guard in order.
func (c *CLI) validate(call *Call) *Result {
	cmd := call.Command
	if !cmd.PinExempt {
		if r := c.checkPin(call); r != nil {
			return r
		}
	}
	if r := checkArgs(call); r != nil {
		return r
	}
	if cmd.NeedsTerminal != nil && cmd.NeedsTerminal(call) && !call.HasTerminal {
		r := refuse(format.NoTerminal, "komodo "+cmd.Name+" changes settings or the install and needs a terminal",
			"hand the command to the person: komodo "+cmd.Name)
		return &r
	}
	return c.checkTamper(call)
}

// checkTamper applies the command's tamper policy when the settings guard has tripped.
func (c *CLI) checkTamper(call *Call) *Result {
	if c.deps.Config == nil {
		return nil
	}
	err := c.deps.Config.Verify()
	if err == nil {
		return nil
	}
	var re config.ReadError
	if !errors.Is(err, config.ErrTampered) && !errors.As(err, &re) {
		r := internalErr("read settings: " + err.Error())
		return &r
	}
	const fix = "komodo config set --accept"
	if call.Command.TamperPolicy == TamperWarn || (call.Command.Name == "config set" && flagBool(call, "accept")) {
		if call.Command.TamperPolicy == TamperWarn {
			fmt.Fprintf(c.errOut, "Warning: settings were edited outside komodo config. Fix: %s\n", fix)
		}
		return nil
	}
	r := refuse(format.ConfigTampered, "settings were edited outside komodo config", fix)
	return &r
}

// resolve fills the repository and expands the command's label.
func (c *CLI) resolve(call *Call) *Result {
	root, ok := findRepoRoot(call.dir())
	if ok {
		call.Repo = RepoRef{ID: repoID(root), Root: root}
		call.CommonDir = commonDir(root)
	}
	if call.Command.NeedsRepo != nil && call.Command.NeedsRepo(call) && !ok {
		r := refuse(format.NoRepo, "komodo "+call.Command.Name+" acts on a repository and "+call.dir()+" is not inside one",
			"run it inside a git repository, or pass -C <path>")
		return &r
	}
	if call.Command.Name == "run" {
		return resolveRun(call)
	}
	return nil
}

// dir returns the folder commands start from: -C, else the working folder.
func (c *Call) dir() string {
	if c.Global.Dir != "" {
		return c.Global.Dir
	}
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

// newCall builds a Call for cmd with the CLI behind it.
func (c *CLI) newCall(cmd *Command, hasTerm bool) *Call {
	return &Call{Command: cmd, HasTerminal: hasTerm, Stdout: c.out, cli: c}
}

// ansiPattern matches the colour and style codes the Output Formatter may add.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// render prints the reply for res and returns its exit code.
func (c *CLI) render(res Result) int {
	code, err := c.print(res)
	if err != nil {
		fmt.Fprintln(c.errOut, "komodo: write reply:", err)
		return int(OutcomeInternal)
	}
	return code
}

// print formats res and writes the envelope or the text; it returns the exit code.
func (c *CLI) print(res Result) (int, error) {
	if res.Raw != "" {
		_, err := fmt.Fprintln(c.out, strings.TrimRight(res.Raw, "\n"))
		return int(res.Outcome), err
	}
	rendered, err := c.format(res)
	if err != nil {
		res = Result{Outcome: OutcomeInternal, Code: string(format.Internal), Reason: "format the reply: " + err.Error()}
		rendered = c.fixedLine()
	}
	if c.json || c.noColor {
		rendered.Report = ansiPattern.ReplaceAllString(rendered.Report, "")
		rendered.Verdict = ansiPattern.ReplaceAllString(rendered.Verdict, "")
	}
	if !c.json {
		_, err := fmt.Fprintln(c.out, rendered.Report)
		return int(res.Outcome), err
	}
	ok := res.Outcome == OutcomeOK || (res.Outcome == OutcomeHeld && !strings.HasPrefix(res.Shape, "error."))
	env := Envelope{V: 1, OK: ok, Verdict: rendered.Verdict, Report: rendered.Report, Data: res.Data}
	if env.Data == nil {
		env.Data = struct{}{}
	}
	if !ok {
		env.Error = &ErrorBody{Code: res.Code, Reason: res.Reason, Fix: res.Fix}
	}
	enc := json.NewEncoder(c.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return int(res.Outcome), enc.Encode(env)
}

// format renders res through the Output Formatter.
func (c *CLI) format(res Result) (format.Rendered, error) {
	if c.deps.Format == nil {
		return format.Rendered{}, errors.New("no formatter")
	}
	shape := res.Shape
	if shape == "" {
		shape = "error." + res.Code
	}
	facts := res.Facts
	if facts == nil {
		facts = struct{}{}
	}
	fb, err := json.Marshal(facts)
	if err != nil {
		return format.Rendered{}, fmt.Errorf("encode facts: %w", err)
	}
	msg, err := json.Marshal(format.Message{Shape: shape, Code: res.Code, Facts: fb})
	if err != nil {
		return format.Rendered{}, fmt.Errorf("encode message: %w", err)
	}
	return c.deps.Format.FormatJSON(msg)
}

// fixedLine is the reply printed when the formatter itself fails.
func (c *CLI) fixedLine() format.Rendered {
	const line = "Komodo could not format its reply. The code is internal."
	return format.Rendered{Verdict: line, Report: line}
}

// PrintError shows a fault to the caller; it implements fault.Printer, which the Error Handler holds.
func (c *CLI) PrintError(f fault.Fault) error {
	if f.Severity == fault.SeverityWarn {
		_, err := fmt.Fprintf(c.errOut, "Warning: %s (%s)\n", f.Reason, f.Code)
		return err
	}
	res, _ := c.faultResult(f)
	_, err := c.print(res)
	return err
}

// refuse builds a declined-on-purpose result (exit 1).
func refuse(code format.Code, reason, fix string) Result {
	return errResult(OutcomeRefused, code, reason, fix)
}

// usageErr builds a bad-command-line result (exit 2).
func usageErr(reason, fix string) Result {
	return errResult(OutcomeUsage, format.Usage, reason, fix)
}

// internalErr builds a harness-failure result (exit 4).
func internalErr(reason string) Result {
	return errResult(OutcomeInternal, format.Internal, reason, "")
}

// notBuilt builds the internal failure a stubbed command returns.
func notBuilt(what string) Result {
	return internalErr(what + " is not built yet")
}

// errResult builds a result whose shape is error.<code>.
func errResult(o Outcome, code format.Code, reason, fix string) Result {
	return Result{
		Outcome: o, Shape: "error." + string(code), Code: string(code), Reason: reason, Fix: fix,
		Facts: format.ErrorFacts{Code: string(code), Reason: reason, Fix: fix},
	}
}

// hasToken reports whether args holds any of the tokens.
func hasToken(args []string, tokens ...string) bool {
	for _, a := range args {
		for _, t := range tokens {
			if a == t {
				return true
			}
		}
	}
	return false
}

// versionResult is the reply to --version.
func versionResult() Result {
	v := core.BinaryVersion()
	return Result{Shape: "reply.version", Facts: format.VersionFacts{Version: v}, Data: VersionData{Version: v}}
}

// VersionData is the data of reply.version.
type VersionData struct {
	Version string `json:"version"`
}
