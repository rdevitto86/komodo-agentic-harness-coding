package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// lensFS holds the lens catalogs: rule data in <lens>.json and rule text in <lens>.md.
//
//go:embed lenses/*.json lenses/*.md
var lensFS embed.FS

// lensOrder lists the lenses in the order Lenses returns them.
var lensOrder = []string{
	"correctness",
	"security",
	"spec",
	"contract",
	"concurrency",
	"platform",
	"config",
	"readiness",
	"quality",
	"economy",
}

// MarkdownOnlyLenses is the number of lenses a diff of only *.md files runs.
const MarkdownOnlyLenses = 3

// markdownLenses are the lenses a diff of only *.md files runs.
var markdownLenses = []string{"correctness", "readiness", "quality"}

// Severity ranks a finding; blocker and major stop the group, minor is counted.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityMajor   Severity = "major"
	SeverityMinor   Severity = "minor"
)

// Applies is the file scope of a rule or a lens: changed-file globs, and regular expressions matched against changed diff
// lines.
type Applies struct {
	Globs   []string `json:"globs"`
	Markers []string `json:"markers"`
}

// Rule is one review rule in a lens catalog.
type Rule struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Applies  Applies  `json:"applies"`
	Check    string   `json:"check"`
	Evidence string   `json:"evidence"`
	Source   string   `json:"source"` // where the rule comes from
}

// When says how a lens is chosen for a diff.
const (
	WhenAlways  = "always"       // every thorough review
	WhenFilter  = "filter"       // the diff meets the lens's globs or markers
	WhenSpec    = "spec_cited"   // a task cites a spec anchor
	WhenEconomy = "economy_only" // the economy profile only
)

// validate checks one rule's fields.
func (r Rule) validate() error {
	switch r.Severity {
	case SeverityBlocker, SeverityMajor, SeverityMinor:
	default:
		return fmt.Errorf("unknown severity %q", r.Severity)
	}

	if r.Check == "" || r.Evidence == "" || r.Source == "" {
		return fmt.Errorf("check, evidence and source are all required")
	}
	if len(r.Applies.Globs) == 0 {
		return fmt.Errorf("no file globs")
	}

	_, err := compileMarkers(r.Applies.Markers)
	return err
}

// Catalog is one lens: when it applies, its rules and the text of each rule.
type Catalog struct {
	Lens    string            `json:"lens"`
	When    string            `json:"when"`
	Applies Applies           `json:"applies"` // the filter of a lens whose When is filter
	Rules   []Rule            `json:"rules"`
	Text    map[string]string `json:"-"` // rule identifier to the text in the Markdown file
	markers []*regexp.Regexp
}

// validate checks the catalog format and compiles its lens markers.
func (c *Catalog) validate() error {
	switch c.When {
	case WhenAlways, WhenSpec, WhenEconomy:
	case WhenFilter:
		if len(c.Applies.Globs)+len(c.Applies.Markers) == 0 {
			return fmt.Errorf("filter lens %s has no globs or markers", c.Lens)
		}
	default:
		return fmt.Errorf("lens %s has unknown when %q", c.Lens, c.When)
	}

	var err error
	if c.markers, err = compileMarkers(c.Applies.Markers); err != nil {
		return err
	}
	if len(c.Rules) == 0 {
		return fmt.Errorf("lens %s has no rules", c.Lens)
	}

	for i, r := range c.Rules {
		if want := fmt.Sprintf("%s.%d", c.Lens, i+1); r.ID != want {
			return fmt.Errorf("rule %d is %q, want %q", i+1, r.ID, want)
		}
		if err := r.validate(); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
		if c.Text[r.ID] == "" {
			return fmt.Errorf("rule %s has no text", r.ID)
		}
	}

	if len(c.Text) != len(c.Rules) {
		return fmt.Errorf(
			"lens %s has text for %d rules but %d rules",
			c.Lens,
			len(c.Text),
			len(c.Rules),
		)
	}
	return nil
}

// matchesFilter reports whether a changed path matches a glob
// or a changed line matches a marker.
func (c Catalog) matchesFilter(d Diff, lines []string) bool {
	for _, g := range c.Applies.Globs {
		if slices.ContainsFunc(d.Paths, func(p string) bool { return MatchGlob(g, p) }) {
			return true
		}
	}
	for _, m := range c.markers {
		if slices.ContainsFunc(lines, m.MatchString) {
			return true
		}
	}
	return false
}

// Profile is the review depth.
type Profile string

const (
	ProfileThorough Profile = "thorough"
	ProfileEconomy  Profile = "economy"
)

// Diff is what lens selection reads.
type Diff struct {
	Paths []string
	Text  string // the unified diff
	Specs bool   // a task cites a spec anchor
}

// Reviewer is the Go side of a Reviewer session.
type Reviewer interface {
	// Lenses returns the lens names to run for a diff under a profile.
	Lenses(d Diff, p Profile) []string
	// AcceptFinding reports whether a submitted finding is verified, and its severity.
	AcceptFinding(f Finding, head, diff LineSource) (Severity, bool, error)
	// AcceptResult rejects a result that skips or repeats a task identifier.
	AcceptResult(tasks []string, r ReviewerResult) error
}

// NewReviewer returns the Go side of a Reviewer session over the compiled catalogs.
// standards reports the severity of a rule compiled from a standards skill; it may be nil.
func NewReviewer(standards func(id string) (Severity, bool)) (Reviewer, error) {
	cats, err := LoadCatalogs()
	if err != nil {
		return nil, err
	}
	return newReviewer(cats, standards), nil
}

// reviewer holds the catalogs and the standards rule lookup.
type reviewer struct {
	cats  []Catalog
	rules map[string]Severity
	std   func(id string) (Severity, bool)
}

// newReviewer indexes the rules of the given catalogs.
func newReviewer(cats []Catalog, standards func(string) (Severity, bool)) reviewer {
	r := reviewer{cats: cats, rules: map[string]Severity{}, std: standards}
	for _, c := range cats {
		for _, rule := range c.Rules {
			r.rules[rule.ID] = rule.Severity
		}
	}
	return r
}

// AcceptFinding refuses an unknown rule or a malformed finding, and reports whether Go found the quoted line.
func (r reviewer) AcceptFinding(f Finding, head, diff LineSource) (Severity, bool, error) {
	sev, ok := r.severity(f.Rule)
	if !ok {
		return "", false, fmt.Errorf("unknown rule %q", f.Rule)
	}
	if strings.TrimSpace(f.File) == "" || f.Line < 1 {
		return "", false, fmt.Errorf("finding needs a file and a line from 1")
	}
	_, found := VerifyFinding(f, head, diff)
	return sev, found, nil
}

// Lenses returns the lens names to run for a diff under a profile, in catalog order.
// A diff of only Markdown files runs 3 lenses. The economy profile runs the merged lens,
// plus security wherever security applies.
func (r reviewer) Lenses(d Diff, p Profile) []string {
	if markdownOnly(d.Paths) {
		return slices.Clone(markdownLenses)
	}

	lines := changedLines(d.Text)
	var out []string

	for _, c := range r.cats {
		var on bool
		switch c.When {
		case WhenAlways:
			on = p != ProfileEconomy
		case WhenSpec:
			on = p != ProfileEconomy && d.Specs
		case WhenFilter:
			on = (p != ProfileEconomy || c.Lens == "security") && c.matchesFilter(d, lines)
		case WhenEconomy:
			on = p == ProfileEconomy
		}
		if on {
			out = append(out, c.Lens)
		}
	}

	return out
}

// severity returns the severity of a known rule, from a lens catalog or the standards rule list.
func (r reviewer) severity(id string) (Severity, bool) {
	if s, ok := r.rules[id]; ok {
		return s, true
	}
	if r.std != nil {
		return r.std(id)
	}
	return "", false
}

// LoadCatalogs reads and checks the compiled lens catalogs, in lens order.
func LoadCatalogs() ([]Catalog, error) {
	files, err := fs.ReadDir(lensFS, "lenses")
	if err != nil {
		return nil, fmt.Errorf("read lens catalogs: %w", err)
	}
	if len(files) != 2*len(lensOrder) {
		return nil, fmt.Errorf("lens folder holds %d files, want %d", len(files), 2*len(lensOrder))
	}

	cats := make([]Catalog, 0, len(lensOrder))
	for _, name := range lensOrder {
		c, err := loadCatalog(name)
		if err != nil {
			return nil, fmt.Errorf("lens %s: %w", name, err)
		}
		cats = append(cats, c)
	}
	return cats, nil
}

// loadCatalog reads one lens's JSON and Markdown files and checks them.
func loadCatalog(name string) (Catalog, error) {
	raw, err := lensFS.ReadFile("lenses/" + name + ".json")
	if err != nil {
		return Catalog{}, err
	}

	var c Catalog
	if err := core.DecodeStrict(raw, &c); err != nil {
		return Catalog{}, fmt.Errorf("decode catalog: %w", err)
	}
	if c.Lens != name {
		return Catalog{}, fmt.Errorf("catalog names lens %q", c.Lens)
	}

	md, err := lensFS.ReadFile("lenses/" + name + ".md")
	if err != nil {
		return Catalog{}, err
	}

	c.Text = ruleText(string(md))
	if err := c.validate(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

// ruleText maps each "## <rule id>" section of a catalog's Markdown to its trimmed text.
func ruleText(md string) map[string]string {
	text := map[string]string{}
	id := ""
	var body []string

	// flush stores the text gathered for the current rule id, then clears it.
	flush := func() {
		if id != "" {
			text[id] = strings.TrimSpace(strings.Join(body, "\n"))
		}
		body = nil
	}

	for _, ln := range strings.Split(md, "\n") {
		if rest, ok := strings.CutPrefix(ln, "## "); ok {
			flush()
			id = strings.TrimSpace(rest)
			continue
		}
		body = append(body, ln)
	}

	flush()
	return text
}

// compileMarkers compiles marker expressions.
func compileMarkers(ms []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(ms))
	for _, m := range ms {
		re, err := regexp.Compile(m)
		if err != nil {
			return nil, fmt.Errorf("marker %q: %w", m, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// changedLines returns the added and removed lines of a unified diff,
// without their marker or the file headers.
func changedLines(text string) []string {
	var out []string
	for _, d := range diffLines(text) {
		out = append(out, d.Text)
	}
	return out
}

// markdownOnly reports whether every path is a Markdown file and there is at least one.
func markdownOnly(paths []string) bool {
	return len(paths) > 0 && !slices.ContainsFunc(
		paths,
		func(p string) bool { return !strings.HasSuffix(p, ".md") },
	)
}
