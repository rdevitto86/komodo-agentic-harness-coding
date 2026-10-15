package cli

import (
	"fmt"
	"regexp"
	"strings"
)

// alwaysTerminal marks a command that never runs without a controlling terminal.
func alwaysTerminal(*Call) bool { return true }

// needsRepo marks a command that acts on one repository.
func needsRepo(*Call) bool { return true }

// needsRepoUnlessAll marks a command that covers the current repository unless --all widens it.
func needsRepoUnlessAll(c *Call) bool { return !c.Global.All }

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
			{Name: "project", Kind: "bool", Help: "Also add the project documents and a pull request template"},
			{Name: "no-project", Kind: "bool", Help: "Stop owning the project files; they stay on disk"},
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
		Name: "run", Summary: "Start, schedule or continue a run, or answer a blocked group", Tier: TierShared,
		Flags: []FlagSpec{
			{Name: "schedule", Kind: "time", Help: "Queue the run for this RFC 3339 time"},
			{Name: "approve", Kind: "bool", Help: "Grant what the blocked group asked for"},
			{Name: "deny", Kind: "bool", Help: "Refuse what the blocked group asked for"},
		},
		NeedsRepo: needsRepo, NeedsEngine: true, TamperPolicy: TamperRefuse, Run: handleRun,
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
		Name: "clean", Summary: "Remove everything that belongs to no open run", Tier: TierShared,
		NeedsEngine: true, TamperPolicy: TamperWarn, Run: handleClean,
	},
}

// commands returns the command table.
func commands() []Command { return tableRows }

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
	epicLabel  = regexp.MustCompile(`^ep-[0-9]+$`)
	groupLabel = regexp.MustCompile(`^tg-[0-9]+\.[0-9]+$`)
	runIDShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
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
		msg = oneTarget(c, "a label or a run id", func(s string) bool { return isLabel(s) || runIDShape.MatchString(s) })
	case "abandon":
		msg = oneTarget(c, "a group label such as tg-1.2", groupLabel.MatchString)
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

// isLabel reports whether s is an epic or group label.
func isLabel(s string) bool { return epicLabel.MatchString(s) || groupLabel.MatchString(s) }

// oneTarget checks that exactly one id of the wanted shape was given.
func oneTarget(c *Call, want string, ok func(string) bool) string {
	if len(c.Args) != 1 {
		return fmt.Sprintf("%s needs one id: %s", c.Command.Name, want)
	}
	if !ok(c.Args[0]) {
		return fmt.Sprintf("%q is not %s", c.Args[0], want)
	}
	return ""
}

// checkRunArgs checks the flag combinations and the id of run.
func checkRunArgs(c *Call) string {
	approve, deny := flagBool(c, "approve"), flagBool(c, "deny")
	scheduled := !flagTime(c, "schedule").IsZero()
	switch {
	case approve && deny:
		return "--approve and --deny cannot combine"
	case (approve || deny) && (c.Global.All || scheduled):
		return "--approve and --deny take an id and cannot combine with --all or --schedule"
	case c.Global.All && len(c.Args) > 0:
		return "--all takes no id"
	case c.Global.All:
		return ""
	}
	return oneTarget(c, "an epic label such as ep-1 or a group label such as tg-1.2", isLabel)
}

// checkStatusArgs checks the optional id and the wait limit of status.
func checkStatusArgs(c *Call) string {
	if flagDuration(c, "timeout") <= 0 {
		return "--timeout must be a positive duration such as 9m"
	}
	if len(c.Args) > 1 {
		return "status takes at most one id"
	}
	if len(c.Args) == 1 && !isLabel(c.Args[0]) && !runIDShape.MatchString(c.Args[0]) {
		return fmt.Sprintf("%q is not a label or a run id", c.Args[0])
	}
	return ""
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
