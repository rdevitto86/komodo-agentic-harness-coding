package task

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// RoleConfig is what the three role adapters read besides the session's result.
type RoleConfig struct {
	Group    string
	Tasks    []TaskSpec
	Items    []agent.Item    // acceptance items of the group's tasks
	Worktree string          // absolute path of the group's worktree
	Files    func() []string // the group's effective file list
	QA       agent.QA
	Reviewer agent.Reviewer
}

// taskIDs lists the identifiers of the group's tasks.
func (c RoleConfig) taskIDs() []string {
	ids := make([]string, len(c.Tasks))
	for i, t := range c.Tasks {
		ids[i] = t.ID
	}
	return ids
}

// taskList renders the tasks, one per line.
func (c RoleConfig) taskList() string {
	var b strings.Builder
	for _, t := range c.Tasks {
		fmt.Fprintf(&b, "- %s: %s (done when: %s)\n", t.ID, t.Title, t.DoneWhen)
	}
	return strings.TrimRight(b.String(), "\n")
}

// builderRole is the builder over agent.ParseBuilderResult.
type builderRole struct {
	cfg RoleConfig
	mu  sync.Mutex
	ans string
}

// SetScopeAnswer stores the text of the scope answer for the builder's next brief.
func (b *builderRole) SetScopeAnswer(s string) {
	b.mu.Lock()
	b.ans = s
	b.mu.Unlock()
}

// Slots fills the builder's brief values for this trip.
func (b *builderRole) Slots(_ context.Context, in RoleInput) (map[string]string, error) {
	files := b.cfg.Files()
	contents, err := fileContents(b.cfg.Worktree, files)
	if err != nil {
		return nil, err
	}

	s := map[string]string{
		"group":         b.cfg.Group,
		"tasks":         b.cfg.taskList(),
		"files":         strings.Join(files, "\n"),
		"file_contents": contents,
	}
	if in.Diff != "" {
		s["diff"] = in.Diff
	}
	if len(in.Findings) > 0 {
		s["findings"] = bullets(in.Findings)
	}

	if u := in.Unfinished; u != nil && len(u.Files) > 0 {
		s["unfinished"] = fmt.Sprintf("Task %s left these changed files uncommitted in the worktree:\n%s", u.Task,
			strings.Join(u.Files, "\n"))
	}

	b.mu.Lock()
	if b.ans != "" {
		s["scope_answer"], b.ans = b.ans, ""
	}
	b.mu.Unlock()
	return s, nil
}

// Judge parses the builder's final call; a request for files outside the list becomes a scope outcome.
func (b *builderRole) Judge(_ context.Context, r llm.Result) (Outcome, error) {
	res, err := agent.ParseBuilderResult(r.Output)
	if err != nil {
		return Outcome{}, &GateError{Text: err.Error(), Schema: true}
	}
	if res.Status == agent.BuilderNeedsScope {
		return Outcome{Kind: OutcomeScope, Paths: res.Paths, Reason: res.Reason}, nil
	}
	return Outcome{Kind: OutcomePass, Summary: []string{res.Summary}}, nil
}

// qaRole is Quality Assurance over agent.QA.
type qaRole struct {
	cfg RoleConfig
}

// AcceptanceItems returns the group's acceptance items, which Quality Assurance's run_check tool may run.
func (t *qaRole) AcceptanceItems() []agent.Item { return slices.Clone(t.cfg.Items) }

// Slots fills Quality Assurance's brief values for this trip.
func (t *qaRole) Slots(_ context.Context, in RoleInput) (map[string]string, error) {
	var acc strings.Builder
	for _, it := range t.cfg.Items {
		fmt.Fprintf(&acc, "- %s: %s", it.ID, it.Text)
		if it.Cmd != "" {
			fmt.Fprintf(&acc, " (command: %s)", it.Cmd)
		}
		acc.WriteString("\n")
	}

	s := map[string]string{
		"group":      t.cfg.Group,
		"tasks":      t.cfg.taskList(),
		"acceptance": strings.TrimRight(acc.String(), "\n"),
	}
	if in.Diff != "" {
		s["diff"] = in.Diff
	}

	if len(in.Findings) > 0 {
		s["checks"] = bullets(in.Findings)
	}

	return s, nil
}

// Judge accepts Quality Assurance's final call and returns the items it marked failed; its confidence scores are
// recorded in the verdict and never change a failed item.
func (t *qaRole) Judge(_ context.Context, r llm.Result) (Outcome, error) {
	res, err := agent.ParseQAResult(r.Output)
	if err != nil {
		return Outcome{}, &GateError{Text: err.Error(), Schema: true}
	}

	items := make([]string, len(t.cfg.Items))

	byID := map[string]agent.Item{}
	for i, it := range t.cfg.Items {
		items[i], byID[it.ID] = it.ID, it
	}
	if err := t.cfg.QA.AcceptResult(t.cfg.taskIDs(), items, res); err != nil {
		return Outcome{}, &GateError{Text: "the result was rejected: " + err.Error()}
	}

	var failed, notMet, listed []string

	for _, ir := range res.Items {
		it := byID[ir.Item]

		switch {
		case ir.Pass:
			continue
		case len(it.Needs) > 0:
			listed = append(
				listed,
				fmt.Sprintf("Quality Assurance: item %s skipped, it needs %s", it.ID, strings.Join(it.Needs, ", ")),
			)
			continue
		}

		failed = append(
			failed,
			fmt.Sprintf(
				"acceptance item %s failed (confidence %d): %s",
				it.ID,
				*ir.Confidence,
				evidenceText(ir.Evidence),
			),
		)
	}

	for _, tc := range res.Tasks {
		if tc.Confirmation == agent.NotConfirmed {
			notMet = append(notMet, tc.Task)
		}
	}

	if len(failed) > 0 || len(notMet) > 0 {
		for _, id := range notMet {
			failed = append(failed, "Quality Assurance did not confirm task "+id)
		}
		return Outcome{Kind: OutcomeReturn, Findings: failed}, nil
	}

	return Outcome{
		Kind: OutcomePass,
		Summary: append(
			[]string{
				fmt.Sprintf("Quality Assurance: %d items, %d tasks confirmed", len(res.Items), len(res.Tasks)),
				"Quality Assurance confidence: " + confidenceText(res.Items),
			},
			listed...),
	}, nil
}

// confidenceText lists the score Quality Assurance gave each item, such as ac-1 90, ac-2 75.
func confidenceText(items []agent.ItemResult) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("%s %d", it.Item, *it.Confidence)
	}

	return strings.Join(parts, ", ")
}

// evidenceText renders the runs behind an answer, one per command.
func evidenceText(runs []agent.EvidenceRun) string {
	parts := make([]string, len(runs))
	for i, e := range runs {
		parts[i] = fmt.Sprintf("%s (exit %d): %s", e.Cmd, e.Exit, e.Excerpt)
	}

	return strings.Join(parts, "\n")
}

// reviewerRole is the reviewer over agent.Reviewer.
type reviewerRole struct {
	cfg  RoleConfig
	cats []agent.Catalog
	mu   sync.Mutex
	subs []Submitted
	diff string
}

// Submit receives one finding filed through submit_finding.
func (r *reviewerRole) Submit(f Submitted) {
	r.mu.Lock()
	r.subs = append(r.subs, f)
	r.mu.Unlock()
}

// Slots fills the reviewer's brief values for this trip and forgets the findings of the last one.
func (r *reviewerRole) Slots(_ context.Context, in RoleInput) (map[string]string, error) {
	r.mu.Lock()
	r.subs, r.diff = nil, in.Diff
	r.mu.Unlock()

	d := agent.Diff{Paths: diffPaths(in.Diff), Text: in.Diff}
	lenses := r.cfg.Reviewer.Lenses(d, agent.ProfileThorough)

	var rules strings.Builder
	for _, c := range r.cats {
		if !slices.Contains(lenses, c.Lens) {
			continue
		}
		for _, rule := range c.Rules {
			fmt.Fprintf(&rules, "- %s (%s): %s\n", rule.ID, rule.Level, c.Text[rule.ID])
		}
	}

	diff := in.Diff
	if diff == "" {
		diff = "(the group changed nothing)"
	}

	s := map[string]string{
		"group":      r.cfg.Group,
		"tasks":      r.cfg.taskList(),
		"files":      strings.Join(r.cfg.Files(), "\n"),
		"diff":       diff,
		"lens":       strings.Join(lenses, ", "),
		"lens_rules": strings.TrimRight(rules.String(), "\n"),
		"checks":     checksText(in.Uncovered),
	}
	if len(in.Findings) > 0 {
		s["findings"] = bullets(in.Findings)
	}

	return s, nil
}

// Judge accepts the reviewer's final call and verifies the findings it filed against the work.
func (r *reviewerRole) Judge(_ context.Context, res llm.Result) (Outcome, error) {
	out, err := agent.ParseReviewerResult(res.Output)
	if err != nil {
		return Outcome{}, &GateError{Text: err.Error(), Schema: true}
	}
	if err := r.cfg.Reviewer.AcceptResult(r.cfg.taskIDs(), out); err != nil {
		return Outcome{}, &GateError{Text: "the result was rejected: " + err.Error()}
	}

	r.mu.Lock()
	subs, diff := slices.Clone(r.subs), r.diff
	r.mu.Unlock()
	head, added := worktreeLines{root: r.cfg.Worktree}, parseAdded(diff)
	d := agent.Diff{Paths: diffPaths(diff), Text: diff}

	var findings, warnings, recorded []string

	var verified []VerifiedFinding

	for _, s := range subs {
		got, aerr := r.cfg.Reviewer.AcceptFinding(s.Finding, head, added, d)
		if aerr != nil || got.Dropped != "" {
			continue
		}

		verified = append(verified, VerifiedFinding{
			Rule: s.Rule, File: s.File, Line: s.Line, Quote: s.Quote, Note: s.Note,
			Proof: s.Proof, Confidence: s.Confidence, Level: got.Level, In: got.In, Bucket: got.Bucket,
		})

		line := fmt.Sprintf("%s (%s) at %s:%d: %s -- %s", s.Rule, got.Level, s.File, s.Line, s.Quote, s.Note)

		switch got.Bucket {
		case agent.BucketBlock:
			findings = append(findings, line)
		case agent.BucketWarn:
			warnings = append(warnings, "warning: "+line)
		default:
			recorded = append(recorded, line)
		}
	}

	var notMet []string

	for _, v := range out.Verdicts {
		switch v.Verdict {
		case agent.VerdictBriefIncomplete:
			return Outcome{
				Kind:   OutcomeBlock,
				Reason: agent.BlockBrief,
				Findings: []string{
					"the reviewer found the brief of task " + v.Task + " incomplete",
				},
			}, nil
		case agent.VerdictNotMet:
			notMet = append(notMet, "the reviewer found task "+v.Task+" not met")
		}
	}

	if len(findings) > 0 || len(notMet) > 0 {
		return Outcome{
			Kind:     OutcomeReturn,
			Findings: append(findings, notMet...),
			Verified: verified,
			Recorded: recorded,
		}, nil
	}

	return Outcome{
		Kind: OutcomePass,
		Summary: append(
			[]string{fmt.Sprintf("reviewer: %d tasks met, %d findings filed", len(out.Verdicts), len(subs))},
			warnings...),
		Verified: verified,
		Recorded: recorded,
	}, nil
}

// worktreeLines reads lines of the files in the worktree.
type worktreeLines struct{ root string }

// Line returns line n of a file in the worktree.
func (w worktreeLines) Line(file string, n int) (string, bool) {
	f, err := os.Open(filepath.Join(w.root, filepath.FromSlash(file)))
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for i := 1; sc.Scan(); i++ {
		if i == n {
			return sc.Text(), true
		}
	}

	return "", false
}

// addedLines holds the added lines of a unified diff by file and new line number.
type addedLines map[string]map[int]string

// Line returns the added line n of a file.
func (a addedLines) Line(file string, n int) (string, bool) {
	s, ok := a[file][n]
	return s, ok
}

// NewRoles builds the builder, Quality Assurance and reviewer over the agent package's pure functions.
func NewRoles(cfg RoleConfig) (map[string]Role, error) {
	if cfg.QA == nil {
		cfg.QA = agent.NewQA()
	}
	if cfg.Reviewer == nil {
		r, err := agent.NewReviewer(nil)
		if err != nil {
			return nil, fmt.Errorf("build the reviewer: %w", err)
		}
		cfg.Reviewer = r
	}

	cats, err := agent.LoadCatalogs()
	if err != nil {
		return nil, fmt.Errorf("load the lens catalogs: %w", err)
	}

	return map[string]Role{
		"builder":  &builderRole{cfg: cfg},
		"qa":       &qaRole{cfg: cfg},
		"reviewer": &reviewerRole{cfg: cfg, cats: cats},
	}, nil
}

// checksText is the reviewer's checks slot: the clean check result, then any added lines no test covers.
func checksText(uncovered []string) string {
	const clean = "Every mechanical check passed before this review."
	if len(uncovered) == 0 {
		return clean
	}
	return clean + "\nAdded lines no test covers:\n" + bullets(uncovered)
}

// bullets renders findings as a list.
func bullets(items []string) string {
	var b strings.Builder
	for _, s := range items {
		b.WriteString("- " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fileContents renders the worktree files that match the file list, up to a size cap.
func fileContents(root string, globs []string) (string, error) {
	// maxFileContents is the most bytes of file text the builder's brief carries.
	const maxFileContents = 200 << 10

	var b strings.Builder

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if d.Name() == ".git" || (rel != "." && strings.HasPrefix(d.Name(), ".") && d.Name() != ".github") {
				return filepath.SkipDir
			}
			return nil
		}

		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() ||
			!slices.ContainsFunc(globs, func(g string) bool { return agent.MatchPath(g, rel) }) {
			return nil
		}

		data, rerr := os.ReadFile(p)
		if rerr != nil || b.Len()+len(data) > maxFileContents {
			return nil
		}

		fmt.Fprintf(&b, "%s\n```\n%s\n```\n\n", rel, strings.TrimRight(string(data), "\n"))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("read the group's files: %w", err)
	}
	if b.Len() == 0 {
		return "(no file of the list exists yet)", nil
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

// diffPaths lists the files a unified diff changes.
func diffPaths(diff string) []string {
	var out []string
	for _, l := range strings.Split(diff, "\n") {
		if rest, ok := strings.CutPrefix(l, "+++ b/"); ok && !slices.Contains(out, rest) {
			out = append(out, rest)
		}
	}
	return out
}

// parseAdded reads the added lines of a unified diff with the new file's line numbers.
func parseAdded(diff string) addedLines {
	out := addedLines{}

	var (
		file string
		n    int
	)

	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			file = strings.TrimPrefix(l, "+++ b/")
		case strings.HasPrefix(l, "@@"):
			if f := strings.Fields(l); len(f) >= 3 {
				start, _, _ := strings.Cut(strings.TrimPrefix(f[2], "+"), ",")
				n, _ = strconv.Atoi(start)
			}
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			if out[file] == nil {
				out[file] = map[int]string{}
			}
			out[file][n] = l[1:]
			n++
		case strings.HasPrefix(l, " "):
			n++
		}
	}

	return out
}
