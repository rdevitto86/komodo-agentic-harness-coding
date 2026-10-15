package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// timeValue is a flag holding an RFC 3339 timestamp.
type timeValue struct{ t time.Time }

// String returns the timestamp in RFC 3339 form, or empty when unset.
func (v *timeValue) String() string {
	if v == nil || v.t.IsZero() {
		return ""
	}
	return v.t.Format(time.RFC3339)
}

// Set parses an RFC 3339 timestamp.
func (v *timeValue) Set(s string) error {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("want an RFC 3339 time such as 2026-10-16T02:00:00-07:00")
	}
	v.t = t
	return nil
}

// Get returns the parsed time.
func (v *timeValue) Get() any { return v.t }

// globalSpecs are the flags every command accepts, in help order.
var globalSpecs = []FlagSpec{
	{Name: "json", Kind: "bool", Help: "Print only the envelope; on when there is no terminal"},
	{Name: "C", Kind: "string", Help: "Act on the repository at this path"},
	{Name: "input", Kind: "string", Help: "The command's arguments as one JSON object, from a file or - for standard input"},
	{Name: "q", Kind: "bool", Help: "Less human text in the report"},
	{Name: "v", Kind: "bool", Help: "More human text in the report"},
	{Name: "no-color", Kind: "bool", Help: "Plain text without colour codes"},
	{Name: "dry-run", Kind: "bool", Help: "On every writing command: print each write, make none"},
	{Name: "help", Kind: "bool", Help: "Show the command's help"},
	{Name: "list", Kind: "bool", Help: "List the ids the command accepts"},
	{Name: "all", Kind: "bool", Help: "Widen a listing or a read to everything the command can show"},
	{Name: "version", Kind: "bool", Help: "Print the running version and nothing else"},
}

// newFlagSet builds the flag set for cmd, or the global flags alone when cmd is nil.
func newFlagSet(cmd *Command) *flag.FlagSet {
	fs := flag.NewFlagSet("komodo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	specs := slices.Clone(globalSpecs)
	if cmd != nil {
		specs = append(specs, cmd.Flags...)
	}
	for _, s := range specs {
		switch s.Kind {
		case "bool":
			fs.Bool(s.Name, s.Default == "true", s.Help)
		case "int":
			n := 0
			fs.IntVar(&n, s.Name, 0, s.Help)
		case "duration":
			d, _ := time.ParseDuration(s.Default)
			fs.DurationVar(&d, s.Name, d, s.Help)
		case "time":
			fs.Var(&timeValue{}, s.Name, s.Help)
		default:
			fs.String(s.Name, s.Default, s.Help)
		}
		if s.Short != "" {
			fs.Var(fs.Lookup(s.Name).Value, s.Short, s.Help)
		}
	}
	return fs
}

// takesValue reports whether a global flag token consumes the next token.
func takesValue(tok string) bool {
	switch strings.TrimLeft(tok, "-") {
	case "C", "input":
		return !strings.Contains(tok, "=")
	}
	return false
}

// splitCommand separates the command words from the other tokens, keeping flags and positionals in order.
func splitCommand(args []string) (words, rest []string) {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "--":
			return words, append(rest, args[i:]...)
		case strings.HasPrefix(tok, "-") && tok != "-":
			rest = append(rest, tok)
			if takesValue(tok) && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		case len(words) == 0, len(words) == 1 && (words[0] == "config" || words[0] == "internal"):
			words = append(words, tok)
		default:
			rest = append(rest, tok)
		}
	}
	return words, rest
}

// parse finds the command, parses every flag and fills the Call; a non-nil Result ends the run.
func (c *CLI) parse(args []string, hasTerm bool) (*Call, *Result) {
	words, rest := splitCommand(args)
	name := strings.Join(words, " ")
	cmd := findCommand(name)
	if name != "" && cmd == nil && name != "config" {
		r := unknownCommand(name)
		return nil, &r
	}
	fs := newFlagSet(cmd)
	call := c.newCall(cmd, hasTerm)
	call.Flags = fs
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		r := flagError(fs, err, name)
		return nil, &r
	}
	if r := c.applyInput(call, fs, &pos); r != nil {
		return nil, r
	}
	call.Args = pos
	call.Global = readGlobals(fs)
	c.json = call.Global.JSON || !hasTerm
	c.noColor = c.noColor || call.Global.NoColor
	switch {
	case call.Global.Version:
		r := versionResult()
		return nil, &r
	case flagBool(call, "help") || name == "" || name == "config":
		r := c.helpResult(call, words)
		return nil, &r
	}
	return call, nil
}

// parseInterleaved parses flags that may follow positional arguments and returns the positionals.
func parseInterleaved(fs *flag.FlagSet, rest []string) ([]string, error) {
	var pos, tail []string
	if i := slices.Index(rest, "--"); i >= 0 {
		rest, tail = rest[:i], rest[i+1:]
	}
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		rest = rest[1:]
	}
	return append(pos, tail...), nil
}

// readGlobals copies the global flag values out of fs.
func readGlobals(fs *flag.FlagSet) GlobalFlags {
	b := func(n string) bool { return fs.Lookup(n).Value.String() == "true" }
	return GlobalFlags{
		JSON: b("json"), Dir: fs.Lookup("C").Value.String(), Input: fs.Lookup("input").Value.String(),
		Quiet: b("q"), Verbose: b("v"), NoColor: b("no-color"), DryRun: b("dry-run"),
		List: b("list"), All: b("all"), Version: b("version"),
	}
}

// applyInput merges the --input object into fs; a flag typed on the command line wins.
func (c *CLI) applyInput(call *Call, fs *flag.FlagSet, pos *[]string) *Result {
	path := fs.Lookup("input").Value.String()
	if path == "" {
		return nil
	}
	obj, err := c.readInput(path)
	if err != nil {
		r := usageErr(fmt.Sprintf("read --input %s: %v", path, err), "pass a JSON object, such as {\"args\": [\"tg-1.2\"]}")
		return &r
	}
	typed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { typed[canonicalName(call, f.Name)] = true })
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := obj[k]
		if k == "args" {
			list, ok := v.([]any)
			if !ok {
				r := usageErr(fmt.Sprintf("--input %s: args must be a list of strings", path), "")
				return &r
			}
			if len(*pos) == 0 {
				for _, a := range list {
					*pos = append(*pos, fmt.Sprint(a))
				}
			}
			continue
		}
		if fs.Lookup(k) == nil || k == "input" {
			r := usageErr(fmt.Sprintf("--input %s: unknown key %q", path, k), closestFix(k, flagNames(fs), call.name()))
			return &r
		}
		if typed[k] {
			continue
		}
		if err := fs.Set(k, fmt.Sprint(v)); err != nil {
			r := usageErr(fmt.Sprintf("--input %s: bad value for %q: %v", path, k, err), "")
			return &r
		}
	}
	return nil
}

// canonicalName maps a short flag name to its long name.
func canonicalName(call *Call, name string) string {
	if call.Command != nil {
		for _, s := range call.Command.Flags {
			if s.Short == name {
				return s.Name
			}
		}
	}
	return name
}

// name returns the command's name, or empty with none.
func (c *Call) name() string {
	if c.Command == nil {
		return ""
	}
	return c.Command.Name
}

// readInput decodes the --input object from a file or standard input.
func (c *CLI) readInput(path string) (map[string]any, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(c.in)
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if obj == nil {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

// flagNames lists the long flag names of fs.
func flagNames(fs *flag.FlagSet) []string {
	var out []string
	fs.VisitAll(func(f *flag.Flag) {
		if len(f.Name) > 1 {
			out = append(out, f.Name)
		}
	})
	return out
}

// flagError turns a flag package error into a usage result naming the closest known flag.
func flagError(fs *flag.FlagSet, err error, cmd string) Result {
	msg := err.Error()
	if bad, ok := strings.CutPrefix(msg, "flag provided but not defined: "); ok {
		bad = strings.TrimLeft(bad, "-")
		return usageErr("unknown flag --"+bad, closestFix(bad, flagNames(fs), cmd))
	}
	return usageErr(msg, helpFix(cmd))
}

// closestFix names the nearest flag within distance 2, or points to help.
func closestFix(bad string, names []string, cmd string) string {
	if n, ok := closest(bad, names); ok {
		return "use --" + n + ", or see " + helpFix(cmd)
	}
	return "see " + helpFix(cmd)
}

// helpFix is the command that shows help for cmd.
func helpFix(cmd string) string {
	if cmd == "" {
		return "komodo --help"
	}
	return "komodo " + cmd + " --help"
}

// closest returns the name at the smallest distance, when that distance is at most 2; ties go to table order.
func closest(bad string, names []string) (string, bool) {
	best, bestD := "", 3
	for _, n := range names {
		if d := distance(bad, n); d < bestD {
			best, bestD = n, d
		}
	}
	return best, bestD <= 2
}

// distance counts the single-letter edits between a and b.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// unknownCommand builds the usage result for a command word outside the table.
func unknownCommand(name string) Result {
	var names []string
	for _, c := range commands() {
		names = append(names, c.Name)
	}
	if n, ok := closest(name, names); ok {
		return usageErr(fmt.Sprintf("unknown command %q", name), "did you mean komodo "+n+"? see komodo --help")
	}
	return usageErr(fmt.Sprintf("unknown command %q", name), "see komodo --help")
}

// flagBool reads a bool flag by long name.
func flagBool(c *Call, name string) bool {
	f := c.Flags.Lookup(name)
	return f != nil && f.Value.String() == "true"
}

// flagString reads a string flag by long name.
func flagString(c *Call, name string) string {
	if f := c.Flags.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// flagDuration reads a duration flag by long name.
func flagDuration(c *Call, name string) time.Duration {
	d, _ := time.ParseDuration(flagString(c, name))
	return d
}

// flagTime reads a time flag by long name; the zero time means unset.
func flagTime(c *Call, name string) time.Time {
	if f := c.Flags.Lookup(name); f != nil {
		if g, ok := f.Value.(flag.Getter); ok {
			t, _ := g.Get().(time.Time)
			return t
		}
	}
	return time.Time{}
}
