package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// tableRows is the command table: the one place parse, validate, help and the drift test read.
var tableRows = []Command{
	{
		Name: "install", Summary: "Install the harness for this machine and repository", Tier: TierTerminal,
		Flags: []FlagSpec{
			{Name: "repo-only", Kind: "bool", Help: "Write only files inside the repository"},
			{Name: "machine-only", Kind: "bool", Help: "Write only the machine parts"},
			{Name: "hosts", Kind: "string", Help: "Override host detection, such as claude,codex"},
			{Name: "no-path", Kind: "bool", Help: "Skip the path step"},
			{Name: "from", Kind: "string", Help: "Install that binary file, with no network"},
			{Name: "force", Kind: "bool", Help: "Overwrite hand-edited owned files"},
			{Name: "remove", Kind: "bool", Help: "Undo the install"},
		},
		NeedsTerminal: alwaysTerminal, PinExempt: true, TamperPolicy: TamperWarn, Run: handleInstall,
	},
	{
		Name: "config get", Summary: "Print settings and the layer that set each", Tier: TierShared,
		TamperPolicy: TamperWarn, Run: handleConfigGet,
	},
	{
		Name: "config set", Summary: "Set, reset or accept settings", Tier: TierTerminal,
		Flags: []FlagSpec{
			{Name: "default", Kind: "bool", Help: "Put each named key back to its built-in value"},
			{Name: "accept", Kind: "bool", Help: "Rehash the settings files after a hand edit"},
		},
		NeedsTerminal: alwaysTerminal, TamperPolicy: TamperRefuse, Run: handleConfigSet,
	},
	{
		Name: "plan", Summary: "Plan task lists from a goal or documents", Tier: TierShared,
		Flags:     []FlagSpec{{Name: "from", Kind: "string", Help: "Plan from this document or folder"}},
		NeedsRepo: needsRepo, NeedsEngine: true, TamperPolicy: TamperRefuse, Run: handlePlan,
	},
	{
		Name:    "run",
		Summary: "Start, schedule or continue a run, answer a blocked group, or override a spent one",
		Tier:    TierShared,
		Flags: []FlagSpec{
			{Name: "schedule", Kind: "time", Help: "Queue the run for this RFC 3339 time"},
			{Name: "approve", Kind: "bool", Help: "Grant what the blocked group asked for"},
			{Name: "deny", Kind: "bool", Help: "Refuse what the blocked group asked for"},
			{
				Name: "override", Kind: "bool", TerminalOnly: true,
				Help: "Give a group with no tries left one final try, once; needs a terminal",
			},
			{
				Name: "minutes", Kind: "int", TerminalOnly: true,
				Help: "With --override: the final try's working time in minutes; skips the prompt",
			},
		},
		NeedsTerminal: func(c *Call) bool { return flagBool(c, "override") },
		NeedsRepo:     needsRepo, NeedsEngine: true, TamperPolicy: TamperRefuse, Run: handleRun,
	},
	{
		Name: "stop", Summary: "Stop a run or remove a queued run", Tier: TierShared,
		NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleStop,
	},
	{
		Name: "replay", Summary: "Play back the recorded events of a run or group", Tier: TierShared,
		NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleReplay,
	},
	{
		Name: "status", Summary: "Show live runs, blocked groups and queued runs", Tier: TierShared,
		Flags: []FlagSpec{
			{Name: "wait", Kind: "bool", Help: "Wait until a group is blocked or done"},
			{Name: "timeout", Kind: "duration", Default: "9m", Help: "The longest wait"},
		},
		NeedsRepo: needsRepoUnlessAll, NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleStatus,
	},
	{
		Name: "abandon", Summary: "Drop a task group for good", Tier: TierShared,
		NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleAbandon,
	},
	{
		Name: "ui", Summary: "Serve the read-only web dashboard", Tier: TierShared,
		NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleUI,
	},
	{
		Name: "doctor", Summary: "Check the install, sandbox, Docker, model links and Komodo block", Tier: TierShared,
		PinExempt: true, TamperPolicy: TamperWarn, Run: handleDoctor,
	},
	{
		Name: "update", Summary: "Update the binary to the newest signed release", Tier: TierTerminal,
		Flags: []FlagSpec{
			{Name: "check", Kind: "bool", Help: "Fetch the release manifest and report; change nothing else"},
			{Name: "to", Kind: "string", Help: "Install that version; may downgrade"},
			{Name: "restart-runs", Kind: "bool", Help: "Move live runs onto the new binary"},
		},
		NeedsTerminal: alwaysTerminal, PinExempt: true, TamperPolicy: TamperWarn, Run: handleUpdate,
	},
	{
		Name: "login", Summary: "Set up model access for one provider", Tier: TierTerminal,
		Flags:         []FlagSpec{{Name: "claude", Kind: "bool", Help: "Set up the Claude subscription link"}},
		NeedsTerminal: alwaysTerminal, TamperPolicy: TamperWarn, Run: handleLogin,
	},
	{
		Name: "clean", Summary: "Remove everything that belongs to no open run", Tier: TierShared,
		NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleClean,
	},
	{
		Name: "internal engine", Summary: "Run the engine process", Tier: TierShared, Hidden: true,
		PinExempt: true, TamperPolicy: TamperWarn, Run: handleEngine,
	},
	{
		Name: "internal tools", Summary: "Run the tool server of one model session", Tier: TierShared,
		Flags: []FlagSpec{
			{Name: "agent", Kind: "string", Help: "The agent the session runs"},
			{Name: "session", Kind: "string", Help: "The session identifier"},
		},
		Hidden: true, PinExempt: true, TamperPolicy: TamperSkip, Run: handleTools,
	},
}

// alwaysTerminal marks a command that never runs without a controlling terminal.
func alwaysTerminal(*Call) bool { return true }

// needsRepo marks a command that acts on one repository.
func needsRepo(*Call) bool { return true }

// needsRepoUnlessAll marks a command that covers the current repository unless --all widens it.
func needsRepoUnlessAll(c *Call) bool { return !c.Global.All }

// commands returns the rows of the 13 commands (config get and config set are two rows); hidden verbs are left out.
func commands() []Command {
	var out []Command
	for _, c := range tableRows {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	return out
}

// findCommand returns the row named name, or nil.
func findCommand(name string) *Command {
	for i := range tableRows {
		if tableRows[i].Name == name {
			return &tableRows[i]
		}
	}
	return nil
}

var (
	groupLabel  = regexp.MustCompile(`^tg-[0-9]+(\.[0-9]+)?$`)
	planIDShape = regexp.MustCompile(`^plan-[0-9a-f]{6}$`)
	runIDShape  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// checkArgs checks flag values, flag combinations and the arguments each command takes.
func checkArgs(c *Call) *Result {
	if c.Global.List {
		return nil
	}
	var msg string
	switch c.Command.Name {
	case "run":
		msg = checkRunArgs(c)
	case "stop", "replay":
		msg = checkTargetArgs(c, false, false)
	case "abandon":
		msg = checkTargetArgs(c, false, true)
	case "status":
		msg = checkStatusArgs(c)
	case "config set":
		msg = checkSetArgs(c)
	case "plan":
		if len(c.Args) == 0 && flagString(c, "from") == "" {
			msg = "plan needs a goal, a --from document, or both"
		}
	case "clean":
		if len(c.Args) > 0 {
			msg = "clean takes no id"
		}
	}
	if msg == "" {
		return nil
	}
	r := usageErr(msg, helpFix(c.Command.Name))
	return &r
}

// checkTargetArgs checks a target of a plan id, a plan id then a group label, or a run id; a bare group label is refused.
// optional allows no target at all, and needLabel makes the group label required.
func checkTargetArgs(c *Call, optional, needLabel bool) string {
	n, name := len(c.Args), c.Command.Name

	switch {
	case n == 0 && optional:
		return ""
	case n == 0 || n > 2:
		return name + " takes a plan id, then a group label, or a run id"
	case groupLabel.MatchString(c.Args[0]):
		return fmt.Sprintf(
			"%q is a group label: name its plan first, as in komodo %s plan-7f3a9c %s", c.Args[0], name, c.Args[0],
		)
	case n == 2 && runIDShape.MatchString(c.Args[0]):
		return "a run id takes no group label"
	case needLabel && n != 2:
		return name + " needs a plan id then a group label"
	}

	return ""
}

// checkRunArgs checks the flag combinations and the id of run.
func checkRunArgs(c *Call) string {
	approve, deny, override := flagBool(c, "approve"), flagBool(c, "deny"), flagBool(c, "override")
	scheduled := !flagTime(c, "schedule").IsZero()
	mins, minutesSet := flagInt(c, "minutes")

	switch {
	case minutesSet && !override:
		return "--minutes goes with --override"
	case minutesSet && (mins < 1 || mins > engine.FinalWallMaxMin):
		return fmt.Sprintf("--minutes must be 1 to %d", engine.FinalWallMaxMin)
	case approve && deny, override && (approve || deny):
		return "--approve, --deny and --override cannot combine"
	case c.Global.All:
		return "run takes a plan id and no --all"
	case (approve || deny || override) && scheduled:
		return "--approve, --deny and --override cannot combine with --schedule"
	case len(c.Args) == 0:
		return "run needs a plan id"
	case groupLabel.MatchString(c.Args[0]):
		return fmt.Sprintf("%q is a group label: name its plan first, as in komodo run plan-7f3a9c %s", c.Args[0], c.Args[0])
	case (approve || deny || override) && len(c.Args) > 2:
		return "--approve, --deny and --override take a plan id and an optional group label, not a prompt"
	case scheduled && len(c.Args) > 1:
		return "--schedule takes a plan id alone"
	case !(approve || deny || override) && len(c.Args) == 2:
		return "a group label needs a prompt, --approve or --deny after it"
	}

	return ""
}

// checkStatusArgs checks the optional id and the wait limit of status.
func checkStatusArgs(c *Call) string {
	if flagDuration(c, "timeout") < time.Second {
		return "--timeout must be at least 1s, such as 9m"
	}
	return checkTargetArgs(c, true, false)
}

// checkSetArgs checks the key=value pairs, --default keys and --accept of config set.
func checkSetArgs(c *Call) string {
	def, accept := flagBool(c, "default"), flagBool(c, "accept")

	switch {
	case def && accept:
		return "--default and --accept cannot combine"
	case accept && len(c.Args) > 0:
		return "--accept takes no key"
	case accept:
		return ""
	case len(c.Args) == 0:
		return "config set needs key=value pairs, --default <key>, or --accept"
	case def:
		return ""
	}

	for _, a := range c.Args {
		if !strings.Contains(a, "=") {
			return fmt.Sprintf("%q is not key=value", a)
		}
	}
	return ""
}
