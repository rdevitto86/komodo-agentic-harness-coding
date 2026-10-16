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

// maxFileContents is the most bytes of file text the builder's brief carries.
const maxFileContents = 200 << 10

// RoleConfig is what the three role adapters read besides the session's result.
type RoleConfig struct {
	Group    string
	Tasks    []TaskSpec
	Items    []agent.Item               // acceptance items of the group's tasks
	Worktree string                     // absolute path of the group's worktree
	Files    func() []string            // the group's effective file list
	Runner   func() agent.CommandRunner // runs a command in the Tester's sandbox; nil means no rerun
	Tester   agent.Tester
	Reviewer agent.Reviewer
}

// NewRoles builds the builder, tester and reviewer over the agent package's pure functions.
func NewRoles(cfg RoleConfig) (map[string]Role, error) {
	if cfg.Tester == nil {
		cfg.Tester = agent.NewTester()
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
		"tester":   &testerRole{cfg: cfg},
		"reviewer": &reviewerRole{cfg: cfg, cats: cats},
	}, nil
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

// bullets renders findings as a list.
func bullets(items []string) string {
	var b strings.Builder
	for _, s := range items {
		b.WriteString("- " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ") + "\n")
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
		"group": b.cfg.Group, "tasks": b.cfg.taskList(), "files": strings.Join(files, "\n"), "file_contents": contents,
	}
	if in.Diff != "" {
		s["diff"] = in.Diff
	}
	if len(in.Findings) > 0 {
		s["findings"] = bullets(in.Findings)
	}
	b.mu.Lock()
	if b.ans != "" {
		s["scope_answer"] = b.ans
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

// fileContents renders the worktree files that match the file list, up to a size cap.
func fileContents(root string, globs []string) (string, error) {
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
		if !d.Type().IsRegular() || !slices.ContainsFunc(globs, func(g string) bool { return agent.MatchPath(g, rel) }) {
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

// testerRole is the tester over agent.Tester.
type testerRole struct {
	cfg RoleConfig
	mu  sync.Mutex
	was string // Go's output after a disputed result; the restart's brief carries it
}

// Slots fills the tester's brief values for this trip.
func (t *testerRole) Slots(_ context.Context, in RoleInput) (map[string]string, error) {
	var acc strings.Builder
	for _, it := range t.cfg.Items {
		fmt.Fprintf(&acc, "- %s: %s", it.ID, it.Text)
		if it.Cmd != "" {
			fmt.Fprintf(&acc, " (command: %s)", it.Cmd)
		}
		acc.WriteString("\n")
	}
	s := map[string]string{"group": t.cfg.Group, "tasks": t.cfg.taskList(), "acceptance": strings.TrimRight(acc.String(), "\n")}
	if in.Diff != "" {
		s["diff"] = in.Diff
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(in.Findings) > 0 || t.was != "" {
		s["checks"] = strings.TrimSpace(bullets(in.Findings) + "\n" + t.was)
	}
	return s, nil
}

// Judge accepts the tester's final call, reruns its failed items and decides what happens next.
func (t *testerRole) Judge(ctx context.Context, r llm.Result) (Outcome, error) {
	res, err := agent.ParseTesterResult(r.Output)
	if err != nil {
		return Outcome{}, &GateError{Text: err.Error(), Schema: true}
	}
	items := make([]string, len(t.cfg.Items))
	byID := map[string]agent.Item{}
	for i, it := range t.cfg.Items {
		items[i], byID[it.ID] = it.ID, it
	}
	if err := t.cfg.Tester.AcceptResult(t.cfg.taskIDs(), items, res); err != nil {
		return Outcome{}, &GateError{Text: "the result was rejected: " + err.Error()}
	}
	var failed, disputed, split, goOut, notMet []string
	for _, ir := range res.Items {
		it := byID[ir.Item]
		if ir.Pass || len(it.Needs) > 0 || t.cfg.Runner == nil {
			continue
		}
		out, text, rerr := t.cfg.Tester.Rerun(ctx, t.cfg.Runner(), it)
		if rerr != nil {
			continue
		}
		switch out {
		case agent.RerunConfirmed:
			failed = append(failed, fmt.Sprintf("acceptance item %s failed: %s\n%s", it.ID, ir.Evidence, text))
		case agent.RerunDisputed:
			disputed = append(disputed, it.ID)
			goOut = append(goOut, fmt.Sprintf("Go reran item %s (%s) twice and it passed both times:\n%s", it.ID, it.Cmd, text))
		case agent.RerunSplit:
			split = append(split, it.ID)
		}
	}
	for _, tc := range res.Tasks {
		if tc.Confirmation == agent.NotConfirmed {
			notMet = append(notMet, tc.Task)
		}
	}
	t.mu.Lock()
	t.was = ""
	t.mu.Unlock()
	switch {
	case len(split) > 0:
		return Outcome{Kind: OutcomeBlock, Reason: "flaky", Findings: split}, nil
	case len(disputed) > 0:
		t.mu.Lock()
		t.was = strings.Join(goOut, "\n")
		t.mu.Unlock()
		return Outcome{Kind: OutcomeRestart, Findings: goOut}, nil
	case len(failed) > 0 || len(notMet) > 0:
		for _, id := range notMet {
			failed = append(failed, "the tester did not confirm task "+id)
		}
		return Outcome{Kind: OutcomeReturn, Findings: failed}, nil
	}
	return Outcome{Kind: OutcomePass, Summary: []string{fmt.Sprintf("tester: %d items, %d tasks confirmed", len(res.Items), len(res.Tasks))}}, nil
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
			fmt.Fprintf(&rules, "- %s (%s): %s\n", rule.ID, rule.Severity, c.Text[rule.ID])
		}
	}
	diff := in.Diff
	if diff == "" {
		diff = "(the group changed nothing)"
	}
	s := map[string]string{
		"group": r.cfg.Group, "tasks": r.cfg.taskList(), "files": strings.Join(r.cfg.Files(), "\n"), "diff": diff,
		"lens": strings.Join(lenses, ", "), "lens_rules": strings.TrimRight(rules.String(), "\n"),
		"checks": "Every mechanical check passed before this review.",
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
	var findings []string
	for _, s := range subs {
		sev, ok, aerr := r.cfg.Reviewer.AcceptFinding(s.Finding, head, added)
		if aerr != nil || !ok || (sev != agent.SeverityBlocker && sev != agent.SeverityMajor) {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s (%s) at %s:%d: %s -- %s", s.Rule, sev, s.File, s.Line, s.Quote, s.Note))
	}
	var notMet []string
	for _, v := range out.Verdicts {
		switch v.Verdict {
		case agent.VerdictBriefIncomplete:
			return Outcome{Kind: OutcomeBlock, Reason: agent.BlockBrief, Findings: []string{"the reviewer found the brief of task " + v.Task + " incomplete"}}, nil
		case agent.VerdictNotMet:
			notMet = append(notMet, "the reviewer found task "+v.Task+" not met")
		}
	}
	if len(findings) > 0 || len(notMet) > 0 {
		return Outcome{Kind: OutcomeReturn, Findings: append(findings, notMet...)}, nil
	}
	return Outcome{Kind: OutcomePass, Summary: []string{fmt.Sprintf("reviewer: %d tasks met, %d findings filed", len(out.Verdicts), len(subs))}}, nil
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
	var file string
	var n int
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
