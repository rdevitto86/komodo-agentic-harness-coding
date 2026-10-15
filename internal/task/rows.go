package task

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// policyJSON is the guard policy compiled into the binary; only this package reads it.
//
//go:embed policy.json
var policyJSON []byte

// RowID names a policy row, such as "G3" or "B1".
type RowID string

// Patterns is one glob or a list of globs; JSON accepts either form.
type Patterns []string

// UnmarshalJSON reads a string or an array of strings.
func (p *Patterns) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*p = Patterns{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("a pattern is a string or a list of strings: %w", err)
	}
	*p = many
	return nil
}

// Row is one guard rule as stored in policy.json.
type Row struct {
	ID          RowID    `json:"id"`          // G1..G6, R1, B1
	Scope       string   `json:"scope"`       // agent name, writers, runners or all
	Kind        string   `json:"kind"`        // tool, path, run or text
	Pattern     Patterns `json:"pattern"`     // what matches; home-relative globs expand at load; text rows hold regular expressions
	Action      string   `json:"action"`      // refuse
	Message     string   `json:"message"`     // template id of the refusal line
	Alternative string   `json:"alternative"` // template id of the allowed alternative
}

// siblingPair says which source file a test file name belongs to.
type siblingPair struct {
	Test   string `json:"test"`   // test file name with {name} and {ext} placeholders
	Source string `json:"source"` // the source file name the test belongs to
}

// siblingFile is the test_siblings part of policy.json.
type siblingFile struct {
	Pairs     []siblingPair `json:"pairs"`
	TestsDirs []string      `json:"tests_dirs"` // a folder name whose files all belong to the crate above it
}

// policyFile is the shape of policy.json.
type policyFile struct {
	V             int               `json:"v"`
	Rows          []Row             `json:"rows"`
	SecretContent []string          `json:"secret_content"`
	TestSiblings  siblingFile       `json:"test_siblings"`
	Templates     map[string]string `json:"templates"`
}

// siblingRule is one compiled test-file pattern.
type siblingRule struct {
	test   *regexp.Regexp
	source string
}

// policy is policy.json after compilation.
type policy struct {
	file     policyFile
	secret   []*regexp.Regexp
	siblings []siblingRule
}

// loadPolicy compiles the embedded policy once.
var loadPolicy = sync.OnceValues(func() (*policy, error) {
	var f policyFile
	if err := core.DecodeStrict(policyJSON, &f); err != nil {
		return nil, fmt.Errorf("read policy.json: %w", err)
	}
	if f.V != 1 {
		return nil, fmt.Errorf("policy.json has version %d, want 1", f.V)
	}
	p := &policy{file: f}
	for _, s := range f.SecretContent {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("compile secret content pattern %q: %w", s, err)
		}
		p.secret = append(p.secret, re)
	}
	for _, pr := range f.TestSiblings.Pairs {
		expr := regexp.QuoteMeta(pr.Test)
		expr = strings.ReplaceAll(expr, `\{name\}`, `(?P<name>[^/]+)`)
		expr = strings.ReplaceAll(expr, `\{ext\}`, `(?P<ext>[^/.]+)`)
		re, err := regexp.Compile("^" + expr + "$")
		if err != nil {
			return nil, fmt.Errorf("compile test sibling %q: %w", pr.Test, err)
		}
		p.siblings = append(p.siblings, siblingRule{test: re, source: pr.Source})
	}
	return p, nil
})

// LoadRows returns the policy rows in file order, with every home-relative pattern expanded under home.
func LoadRows(home string) ([]Row, error) {
	p, err := loadPolicy()
	if err != nil {
		return nil, err
	}
	rows := make([]Row, len(p.file.Rows))
	for i, r := range p.file.Rows {
		r.Pattern = expandHome(r.Pattern, home)
		rows[i] = r
	}
	return rows, nil
}

// expandHome rewrites each pattern that starts with "~/" to an absolute pattern under home; an empty home leaves them unmatchable.
func expandHome(pats Patterns, home string) Patterns {
	out := make(Patterns, len(pats))
	for i, s := range pats {
		neg := strings.HasPrefix(s, "!")
		body := strings.TrimPrefix(s, "!")
		if rest, ok := strings.CutPrefix(body, "~/"); ok {
			if home == "" {
				body = "/nonexistent-home/" + rest
			} else {
				body = path.Join(filepath.ToSlash(home), rest)
			}
		}
		if neg {
			body = "!" + body
		}
		out[i] = body
	}
	return out
}

// KnownRows returns the identifiers of the rows an agent's Guard list may name, for the Configuration Manager's registry check.
func KnownRows() map[string]bool {
	out := map[string]bool{}
	p, err := loadPolicy()
	if err != nil {
		return out
	}
	for _, r := range p.file.Rows {
		out[string(r.ID)] = true
	}
	return out
}

// Known returns the guard rows and tool names the Configuration Manager checks the agent registry against.
func Known() config.Known {
	return config.Known{GuardRows: KnownRows(), Tools: KnownTools()}
}

// mandatoryRows are the rows every agent inherits whatever its permissions list.
var mandatoryRows = []RowID{"G1", "G2", "G3", "G4", "G5", "G6"}

// writes reports whether the permissions list a tool that changes files.
func (p Permissions) writes() bool {
	return slices.Contains(p.Tools, "edit") || slices.Contains(p.Tools, "write")
}

// selects reports whether the row binds this agent: by scope, by being named in its guard list, or by being a mandatory row.
func (r Row) selects(b Binding) bool {
	if slices.Contains(mandatoryRows, r.ID) || slices.Contains(b.Permissions.Guard, r.ID) {
		return true
	}
	switch r.Scope {
	case "all":
		return true
	case "writers":
		return b.Permissions.writes()
	case "runners":
		return slices.Contains(b.Permissions.Tools, "run")
	default:
		return r.Scope == b.Agent
	}
}

// render fills a refusal template by id; an unknown id yields a plain line naming the id, never an empty reply.
func (p *policy) render(id string, vars map[string]string) string {
	t, ok := p.file.Templates[id]
	if !ok {
		return "Refused: " + id
	}
	for k, v := range vars {
		t = strings.ReplaceAll(t, "{{"+k+"}}", v)
	}
	return t
}

// siblingOf reports whether p is a test file that sits beside a source file in files.
func (p *policy) siblingOf(rel string, files []string) bool {
	dir, base := path.Split(rel)
	for _, rule := range p.siblings {
		m := rule.test.FindStringSubmatch(base)
		if m == nil {
			continue
		}
		src := rule.source
		for i, n := range rule.test.SubexpNames() {
			if n != "" {
				src = strings.ReplaceAll(src, "{"+n+"}", m[i])
			}
		}
		if inFiles(dir+src, files) {
			return true
		}
	}
	segs := strings.Split(rel, "/")
	for i, s := range segs[:len(segs)-1] {
		if !slices.Contains(p.file.TestSiblings.TestsDirs, s) {
			continue
		}
		crate := strings.Join(segs[:i], "/")
		if crate != "" {
			crate += "/"
		}
		if inFiles(crate+"src/lib.rs", files) || inFiles(crate+"src/main.rs", files) || underStatic(crate, files) {
			return true
		}
	}
	return false
}

// inFiles reports whether rel is one of the group's files or matches one of its globs.
func inFiles(rel string, files []string) bool {
	return slices.ContainsFunc(files, func(f string) bool { return f == rel || agent.MatchPath(f, rel) })
}

// underStatic reports whether any file entry names a folder inside dir before its first wildcard.
func underStatic(dir string, files []string) bool {
	if dir == "" {
		return false
	}
	for _, f := range files {
		if i := strings.IndexAny(f, "*?["); i >= 0 {
			f = f[:i]
		}
		if strings.HasPrefix(f, dir) {
			return true
		}
	}
	return false
}

// errNoPolicy is returned when the embedded policy cannot be read.
var errNoPolicy = errors.New("the guard policy cannot be read")
