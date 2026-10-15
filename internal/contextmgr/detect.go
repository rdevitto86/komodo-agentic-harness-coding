package contextmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// Class is one kind of command the Task Manager and the agents' run tool execute.
type Class string

// The command classes.
const (
	ClassSetup   Class = "setup"
	ClassBuild   Class = "build"
	ClassTest    Class = "test"
	ClassLint    Class = "lint"
	ClassFormat  Class = "format"
	ClassGitRead Class = "git-read" // git status, diff, log and show; always on
)

// Command is one detected command, run without a shell.
type Command struct {
	Class  Class
	Argv   []string
	Marker string // the file that produced it; "Makefile" when a target won
}

// CommandSet is the detected commands of one repo and the hash they are cached under.
type CommandSet struct {
	Commands   []Command
	MarkerHash string // hash of the marker files; the cache key
}

// classOrder lists the detected classes in rendering order.
var classOrder = []Class{ClassSetup, ClassBuild, ClassTest, ClassLint, ClassFormat}

// markerRow is the commands one marker file implies; a class with no entry stays empty.
type markerRow struct {
	file string
	cmds map[Class][]string
}

// markerRows lists the language markers in priority order.
var markerRows = []markerRow{
	{"go.mod", map[Class][]string{
		ClassSetup: {"go", "mod", "download"}, ClassBuild: {"go", "build", "./..."}, ClassTest: {"go", "test", "./..."},
		ClassLint: {"go", "vet", "./..."}, ClassFormat: {"gofmt", "-l", "."},
	}},
	{"package.json", map[Class][]string{
		ClassSetup: {"npm", "ci"}, ClassBuild: {"npm", "run", "build"}, ClassTest: {"npm", "test"},
		ClassLint: {"npm", "run", "lint"}, ClassFormat: {"npm", "run", "format"},
	}},
	{"Cargo.toml", map[Class][]string{
		ClassSetup: {"cargo", "fetch"}, ClassBuild: {"cargo", "build"}, ClassTest: {"cargo", "test"},
		ClassLint: {"cargo", "clippy"}, ClassFormat: {"cargo", "fmt", "--check"},
	}},
	{"pyproject.toml", map[Class][]string{
		ClassTest: {"pytest"}, ClassLint: {"ruff", "check"}, ClassFormat: {"ruff", "format", "--check"},
	}},
}

// makeTargets names the Makefile target of each class.
var makeTargets = map[Class]string{ClassSetup: "setup", ClassBuild: "build", ClassTest: "test", ClassLint: "lint", ClassFormat: "fmt"}

// gitRead lists the read-only git commands that are always on.
var gitRead = [][]string{{"git", "status"}, {"git", "diff"}, {"git", "log"}, {"git", "show"}}

// targetRe finds a Makefile rule line and captures its target.
var targetRe = regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+)\s*:([^=]|$)`)

// markerFiles returns the marker files the tree holds at its root, Makefile first, then in table order.
func markerFiles(t Tree) []string {
	have := map[string]bool{}
	for _, p := range t.Paths() {
		have[p] = true
	}
	var out []string
	for _, f := range append([]string{"Makefile"}, rowFiles()...) {
		if have[f] {
			out = append(out, f)
		}
	}
	return out
}

// rowFiles lists the marker file of every language row.
func rowFiles() []string {
	out := make([]string, len(markerRows))
	for i, r := range markerRows {
		out[i] = r.file
	}
	return out
}

// markerHash hashes the names and contents of the marker files.
func markerHash(t Tree) string {
	h := sha256.New()
	for _, f := range markerFiles(t) {
		b, _ := t.ReadFile(f)
		h.Write([]byte(f + "\x00"))
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Detect finds the repo's commands from its marker files; a Makefile target beats a language default.
func Detect(t Tree) CommandSet { return detectWith(t, markerHash(t)) }

// detectWith is Detect with the marker hash already computed.
func detectWith(t Tree, hash string) CommandSet {
	files := markerFiles(t)
	targets := map[string]bool{}
	for _, f := range files {
		if f == "Makefile" {
			b, _ := t.ReadFile(f)
			for _, m := range targetRe.FindAllStringSubmatch(string(b), -1) {
				targets[m[1]] = true
			}
		}
	}
	cs := CommandSet{MarkerHash: hash}
	for _, c := range classOrder {
		if targets[makeTargets[c]] {
			cs.Commands = append(cs.Commands, Command{Class: c, Argv: []string{"make", makeTargets[c]}, Marker: "Makefile"})
			continue
		}
		for _, row := range markerRows {
			if argv, ok := row.cmds[c]; ok && containsStr(files, row.file) {
				cs.Commands = append(cs.Commands, Command{Class: c, Argv: append([]string(nil), argv...), Marker: row.file})
				break
			}
		}
	}
	for _, argv := range gitRead {
		cs.Commands = append(cs.Commands, Command{Class: ClassGitRead, Argv: argv})
	}
	return cs
}

// containsStr reports whether list holds s.
func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// detectLanguages lists the languages of the tree's files by extension, sorted by name.
func detectLanguages(t Tree, langs []langPattern) []string {
	set := map[string]bool{}
	for _, p := range t.Paths() {
		if name, _ := languageOf(p, langs); name != "" {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// renderProfile renders the repo_profile slot: the languages, then one line per detected command class.
func renderProfile(languages []string, cs CommandSet) string {
	var b strings.Builder
	if len(languages) == 0 {
		b.WriteString("Languages: none\n")
	} else {
		b.WriteString("Languages: " + strings.Join(languages, ", ") + "\n")
	}
	b.WriteString("Commands:")
	found := false
	for _, c := range append(append([]Class(nil), classOrder...), ClassGitRead) {
		var cmds []string
		for _, cmd := range cs.Commands {
			if cmd.Class == c {
				cmds = append(cmds, strings.Join(cmd.Argv, " "))
			}
		}
		if len(cmds) > 0 {
			found = true
			b.WriteString("\n  " + string(c) + ": " + strings.Join(cmds, ", "))
		}
	}
	if !found {
		b.WriteString(" none")
	}
	return b.String()
}
