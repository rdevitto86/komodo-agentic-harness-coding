package intake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// PlanDraft is the planner's result: every task-list file of the plan, in memory.
type PlanDraft struct {
	Files []DraftFile `json:"files"`
}

// DraftFile is one task-list file of a draft.
type DraftFile struct {
	Path    string `json:"path"`    // path under docs/backlog/, such as epic-2/tg-2.1/TG.md
	Content string `json:"content"` // the whole file
}

// PlannerSession is one live planner session.
type PlannerSession interface {
	// Send gives the session a brief or lint failure text and returns its next draft.
	Send(ctx context.Context, text string) (PlanDraft, error)
	// Close ends the session.
	Close() error
}

// StartSession opens a planner session through the model client.
type StartSession func(ctx context.Context) (PlannerSession, error)

// PlanResult says what a plan wrote.
type PlanResult struct {
	Epic    int
	Written []string          // paths of the files written
	Groups  []string          // labels of the groups written, in dependency order
	Hashes  map[string]string // card hash of each group written, by label
}

// PlanBlockedError ends a plan whose third draft failed lint; nothing was written.
type PlanBlockedError struct {
	Findings  []Finding // the findings of the third draft
	DraftPath string    // folder that holds the last failing draft for inspection
}

// Error returns the failure with the finding count.
func (e PlanBlockedError) Error() string {
	return fmt.Sprintf("plan blocked: the third draft failed lint with %d findings", len(failing(e.Findings)))
}

// LockedError is returned when a group is in a run; Run names that run.
type LockedError struct{ Run string }

// Error names the run that holds the group.
func (e LockedError) Error() string {
	return fmt.Sprintf("a group of the plan is in run %s; it is left unchanged", e.Run)
}

// writeError marks a failure to write the task-list folders.
type writeError struct{ err error }

// draftPathEpic matches the epic folder at the start of a draft path.
var draftPathEpic = regexp.MustCompile(`^epic-(\d+)/`)

// Error returns the wrapped failure.
func (e writeError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped failure.
func (e writeError) Unwrap() error { return e.err }

// LintBacklog lints a draft against the stored task lists of the plan in progress; it is the planner's lint_backlog tool.
func (h *Handler) LintBacklog(d PlanDraft) []Finding {
	h.stateMu.Lock()
	cur := h.current
	h.stateMu.Unlock()
	if cur == nil {
		return LintDraft(d, nil, h.protected(), nil)
	}
	return LintDraft(d, cur.existing, h.protected(), cur.tracked)
}

// Plan runs the planner session, lints its draft and writes files only when lint passes.
func (h *Handler) Plan(ctx context.Context, r PlanRequest) (PlanResult, error) {
	// noGoalText stands in for the goal slot when a request names only documents.
	const noGoalText = "Plan the work described in the source documents."

	h.mu.Lock()
	defer h.mu.Unlock()

	if strings.TrimSpace(r.Goal) == "" && len(r.Docs) == 0 {
		return PlanResult{}, errors.New("a plan needs a goal, a document or both")
	}

	tree, err := h.d.Tree(ctx)
	if err != nil {
		return PlanResult{}, fmt.Errorf("read the base commit: %w", err)
	}

	specs, err := readDocs(tree, r.Docs)
	if err != nil {
		return PlanResult{}, err
	}
	if err := restoreOrphans(h.d.Root); err != nil {
		return PlanResult{}, writeError{fmt.Errorf("restore interrupted plan: %w", err)}
	}

	existing, _, err := Load(h.d.Root)
	if err != nil {
		return PlanResult{}, err
	}

	locked, err := h.lockedGroups(existing, r.Epic)
	if err != nil {
		return PlanResult{}, err
	}

	pl := &planState{existing: existing, tracked: tree.Paths()}

	h.stateMu.Lock()
	h.current = pl

	h.stateMu.Unlock()
	defer func() { h.stateMu.Lock(); h.current = nil; h.stateMu.Unlock() }()

	if h.d.Limits.WallMin > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(h.d.Limits.WallMin)*h.wallUnit)
		defer cancel()
	}

	goal := strings.TrimSpace(r.Goal)
	if goal == "" {
		goal = noGoalText
	}

	c, err := h.d.Context.GetContext(ctx, contextmgr.Request{
		Agent: "planner", Tree: tree, Chain: h.d.Chain,
		Slots: contextmgr.Values{
			contextmgr.Goal:    goal,
			contextmgr.Specs:   specs,
			contextmgr.Backlog: backlogSummary(existing, locked),
		},
	})
	if err != nil {
		return PlanResult{}, fmt.Errorf("build the planner brief: %w", err)
	}

	sess, err := h.d.Start(ctx)
	if err != nil {
		return PlanResult{}, h.sessionError(ctx, err)
	}

	defer func() { _ = sess.Close() }()

	return h.lintLoop(ctx, sess, c.Brief.Text(), r, pl, locked)
}

// lockedGroups maps each group of the amended epic that is in a run to that run; a new epic has none.
func (h *Handler) lockedGroups(existing []Epic, epic int) (map[string]string, error) {
	locked := map[string]string{}
	if epic == 0 {
		return locked, nil
	}

	i := slices.IndexFunc(existing, func(e Epic) bool { return e.Number == epic })
	if i < 0 {
		return nil, fmt.Errorf("epic-%d does not exist", epic)
	}

	for _, g := range existing[i].Groups {
		run, in, err := h.d.Locks.InRun(g.ID)
		if err != nil {
			return nil, fmt.Errorf("check whether %s is in a run: %w", g.Name, err)
		}
		if in {
			locked[g.Name] = run
		}
	}

	return locked, nil
}

// lintLoop sends the brief, lints each draft and sends failures back until a draft passes or the third fails.
func (h *Handler) lintLoop(
	ctx context.Context,
	sess PlannerSession,
	brief string,
	r PlanRequest,
	pl *planState,
	locked map[string]string,
) (PlanResult, error) {
	// maxDrafts is how many drafts a plan may fail lint with before it is blocked: the first plus 2 returns.
	const maxDrafts = 3

	protected := h.protected()
	base := &baseCheck{open: h.d.OpenBase}

	defer base.close()

	text := brief
	for attempt := 1; ; attempt++ {
		draft, err := sess.Send(ctx, text)
		if err != nil {
			return PlanResult{}, h.sessionError(ctx, err)
		}
		if run := lockedInDraft(draft, locked, r.Epic); run != "" {
			return PlanResult{}, LockedError{Run: run}
		}

		found := append(
			LintDraft(draft, pl.existing, protected, pl.tracked),
			epicNumberFindings(draft, pl.existing, r.Epic)...,
		)

		found = sortFindings(found)
		if len(failing(found)) == 0 {
			more, err := base.findings(ctx, draft)
			if err != nil && ctx.Err() != nil {
				return PlanResult{}, h.sessionError(ctx, err)
			}
			if err != nil {
				return PlanResult{}, err
			}

			found = append(found, more...)
		}
		if len(failing(found)) == 0 {
			return h.write(draft, pl, r.Epic)
		}
		if attempt == maxDrafts {
			return PlanResult{}, PlanBlockedError{Findings: found, DraftPath: h.saveFailed(draft)}
		}

		text = failureText(found)
	}
}

// protected returns the protected paths, or none when no source is wired.
func (h *Handler) protected() []string {
	if h.d.Protected == nil {
		return nil
	}
	return h.d.Protected()
}

// sessionError turns a planner session failure into the deadline error when the wall clock ended, else a wrapped failure.
func (h *Handler) sessionError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("planner wall clock of %d minutes ended: %w", h.d.Limits.WallMin, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("planner session: %w", err)
}

// saveFailed writes a failing draft under docs/backlog/.plan-failed-<random>/ and returns that folder, or "" when it cannot
// be written.
func (h *Handler) saveFailed(d PlanDraft) string {
	base := filepath.Join(h.d.Root, filepath.FromSlash(backlogDir))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return ""
	}

	dir, err := os.MkdirTemp(base, ".plan-failed-")
	if err != nil {
		return ""
	}

	for _, f := range d.Files {
		safe := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(f.Path)), "/")
		if safe == "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(safe))
		if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
			_ = os.WriteFile(p, []byte(f.Content), 0o644)
		}
	}

	return dir
}

// DraftFromResult copies the planner's final call into a draft.
func DraftFromResult(r agent.PlannerResult) PlanDraft {
	d := PlanDraft{Files: make([]DraftFile, len(r.Files))}
	for i, f := range r.Files {
		d.Files[i] = DraftFile{Path: f.Path, Content: f.Content}
	}
	return d
}

// lockedInDraft returns the run of a group in a run that the draft rewrites, or "".
func lockedInDraft(d PlanDraft, locked map[string]string, epic int) string {
	for _, f := range d.Files {
		if m := pathShape.FindStringSubmatch(f.Path); m != nil {
			if n, _ := strconv.Atoi(m[1]); n != epic {
				continue
			}
			if run, ok := locked[m[2]]; ok {
				return run
			}
		}
	}

	return ""
}

// epicNumberFindings fails a draft whose epic is not the one the request allows: highest plus 1 for a new epic, the named
// epic for an amendment.
func epicNumberFindings(d PlanDraft, existing []Epic, epic int) []Finding {
	highest := 0
	for _, e := range existing {
		highest = max(highest, e.Number)
	}

	want := epic
	if epic == 0 {
		want = highest + 1
	}

	var found []Finding
	for _, f := range d.Files {
		m := draftPathEpic.FindStringSubmatch(f.Path)
		if m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n != want {
			found = append(found, grammar(f.Path, fmt.Sprintf("this plan may write epic-%d only", want)))
		}
	}

	return found
}

// failureText renders the failing findings as the text sent back into the planner session.
func failureText(found []Finding) string {
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"The draft failed lint with %d findings. Fix every one and return the whole plan again.\n",
		len(failing(found)),
	)

	for _, f := range failing(found) {
		fmt.Fprintf(&b, "- %s %s: %s\n", f.Rule, f.Path, f.Message)
	}

	return b.String()
}

// fails returns the failing findings.
func failing(found []Finding) []Finding {
	var out []Finding
	for _, f := range found {
		if f.Severity == SeverityFail {
			out = append(out, f)
		}
	}
	return out
}
