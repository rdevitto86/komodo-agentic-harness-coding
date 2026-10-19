package contextmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
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
	Class   Class
	Argv    []string
	Marker  string // the file that produced it; "Makefile" when a target won
	PerFile bool   // true when the caller appends the edited file paths as final arguments
}

// CommandSet is the detected commands of one repo and the hash they are cached under.
type CommandSet struct {
	Commands   []Command
	MarkerHash string // hash of the marker files; the cache key
}

// cmdSpec is the argv of one class under one marker.
type cmdSpec struct {
	argv    []string
	perFile bool
}

// markerRow is the commands one marker file implies; a class with no entry stays empty.
type markerRow struct {
	file string
	cmds func(t Tree) map[Class]cmdSpec
}

var (
	// lookPath finds a program on PATH; tests replace it.
	lookPath = exec.LookPath
	// classOrder lists the detected classes in rendering order.
	classOrder = []Class{ClassSetup, ClassBuild, ClassTest, ClassLint, ClassFormat}
	// markerRows lists the language markers in priority order.
	markerRows = []markerRow{
		{"go.mod", goCmds},
		{"package.json", nodeCmds},
		{"Cargo.toml", fixedCmds(map[Class]cmdSpec{
			ClassSetup:  {argv: []string{"cargo", "fetch"}},
			ClassBuild:  {argv: []string{"cargo", "build"}},
			ClassTest:   {argv: []string{"cargo", "test"}},
			ClassLint:   {argv: []string{"cargo", "clippy"}},
			ClassFormat: {argv: []string{"cargo", "fmt", "--"}, perFile: true},
		})},
		{"pyproject.toml", fixedCmds(map[Class]cmdSpec{
			ClassTest:   {argv: []string{"pytest"}},
			ClassLint:   {argv: []string{"ruff", "check"}},
			ClassFormat: {argv: []string{"ruff", "format"}, perFile: true},
		})},
	}
	// golangciConfigs lists the golangci-lint configuration file names, looked up at the repository root.
	golangciConfigs = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}
	// makeTargets names the Makefile target of each class.
	makeTargets = map[Class]string{
		ClassSetup:  "setup",
		ClassBuild:  "build",
		ClassTest:   "test",
		ClassLint:   "lint",
		ClassFormat: "fmt",
	}
	// gitRead lists the read-only git commands that are always on.
	gitRead = [][]string{{"git", "status"}, {"git", "diff"}, {"git", "log"}, {"git", "show"}}
	// targetRe finds a Makefile rule line and captures its target.
	targetRe = regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+)\s*:([^=]|$)`)
	// nodeScripts maps each class to the npm command that runs it.
	nodeScripts = map[Class][]string{
		ClassBuild: {"npm", "run", "build"},
		ClassTest:  {"npm", "test"},
		ClassLint:  {"npm", "run", "lint"},
	}
	// nodeScriptNames maps each class to the package.json script that must exist for it to apply.
	nodeScriptNames = map[Class]string{
		ClassBuild: "build",
		ClassTest:  "test",
		ClassLint:  "lint",
	}
)

// Detect finds the repo's commands from its marker files; a Makefile target beats a language
// default.
func Detect(t Tree) CommandSet { return detectWith(t, markerHash(t)) }

// fixedCmds returns a command table that does not depend on the tree.
func fixedCmds(m map[Class]cmdSpec) func(Tree) map[Class]cmdSpec {
	return func(Tree) map[Class]cmdSpec { return m }
}

// goCmds returns the Go commands; lint is golangci-lint when the tree has its configuration file and the tool is on PATH.
func goCmds(t Tree) map[Class]cmdSpec {
	lint := []string{"go", "vet", "./..."}
	if golangciReady(t) {
		lint = []string{"golangci-lint", "run"}
	}

	return map[Class]cmdSpec{
		ClassSetup:  {argv: []string{"go", "mod", "download"}},
		ClassBuild:  {argv: []string{"go", "build", "./..."}},
		ClassTest:   {argv: []string{"go", "test", "./..."}},
		ClassLint:   {argv: lint},
		ClassFormat: {argv: []string{"gofmt", "-w"}, perFile: true},
	}
}

// golangciReady reports whether the tree has a golangci-lint configuration file and the tool is on PATH.
func golangciReady(t Tree) bool {
	if len(golangciFiles(t)) == 0 {
		return false
	}

	_, err := lookPath("golangci-lint")
	return err == nil
}

// golangciFiles returns the golangci-lint configuration files the tree holds at its root.
func golangciFiles(t Tree) []string {
	have := map[string]bool{}
	for _, p := range t.Paths() {
		have[p] = true
	}

	var out []string
	for _, f := range golangciConfigs {
		if have[f] {
			out = append(out, f)
		}
	}

	return out
}

// nodeCmds fills a class only when package.json defines its script; setup needs none.
func nodeCmds(t Tree) map[Class]cmdSpec {
	out := map[Class]cmdSpec{ClassSetup: {argv: []string{"npm", "ci"}}}

	raw, err := t.ReadFile("package.json")
	if err != nil {
		return out
	}

	var pkg struct {
		Scripts         map[string]string          `json:"scripts"`
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if json.Unmarshal(raw, &pkg) != nil {
		return out
	}

	for c, argv := range nodeScripts {
		if _, ok := pkg.Scripts[nodeScriptNames[c]]; ok {
			out[c] = cmdSpec{argv: argv}
		}
	}

	_, dep := pkg.Dependencies["prettier"]
	_, dev := pkg.DevDependencies["prettier"]

	if _, ok := pkg.Scripts["format"]; dep || dev {
		out[ClassFormat] = cmdSpec{argv: []string{"npx", "prettier", "--write"}, perFile: true}
	} else if ok {
		out[ClassFormat] = cmdSpec{argv: []string{"npm", "run", "format", "--"}, perFile: true}
	}

	return out
}

// markerFiles returns the marker files the tree holds at its root, Makefile first, then in table
// order, then the golangci-lint configuration files.
func markerFiles(t Tree) []string {
	have := map[string]bool{}
	for _, p := range t.Paths() {
		have[p] = true
	}

	var out []string
	for _, f := range slices.Concat([]string{"Makefile"}, rowFiles(), golangciConfigs) {
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

// markerHash hashes the names and contents of the marker files, and whether golangci-lint is on PATH when the tree
// configures it.
func markerHash(t Tree) string {
	h := sha256.New()

	for _, f := range markerFiles(t) {
		b, _ := t.ReadFile(f)
		h.Write([]byte(f + "\x00"))
		h.Write(b)
		h.Write([]byte{0})
	}

	if len(golangciFiles(t)) > 0 {
		h.Write([]byte("golangci-lint\x00"))
		if golangciReady(t) {
			h.Write([]byte{1})
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

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

	tables := map[string]map[Class]cmdSpec{}

	for _, row := range markerRows {
		if slices.Contains(files, row.file) {
			tables[row.file] = row.cmds(t)
		}
	}

	cs := CommandSet{MarkerHash: hash}

	for _, c := range classOrder {
		if targets[makeTargets[c]] {
			cs.Commands = append(cs.Commands, Command{
				Class:  c,
				Argv:   []string{"make", makeTargets[c]},
				Marker: "Makefile",
			})
			continue
		}

		for _, row := range markerRows {
			if spec, ok := tables[row.file][c]; ok {
				cs.Commands = append(cs.Commands, Command{
					Class:   c,
					Argv:    slices.Clone(spec.argv),
					Marker:  row.file,
					PerFile: spec.perFile,
				})
				break
			}
		}
	}

	for _, argv := range gitRead {
		cs.Commands = append(cs.Commands, Command{Class: ClassGitRead, Argv: argv})
	}

	return cs
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
		fmt.Fprintf(&b, "Languages: %s\n", strings.Join(languages, ", "))
	}
	b.WriteString("Commands:")

	found := false

	for _, c := range append(slices.Clone(classOrder), ClassGitRead) {
		var cmds []string

		for _, cmd := range cs.Commands {
			if cmd.Class != c {
				continue
			}
			text := strings.Join(cmd.Argv, " ")
			if cmd.PerFile {
				text += " <file>"
			}
			cmds = append(cmds, text)
		}

		if len(cmds) > 0 {
			found = true
			fmt.Fprintf(&b, "\n  %s: %s", c, strings.Join(cmds, ", "))
		}
	}

	if !found {
		b.WriteString(" none")
	}

	return b.String()
}
