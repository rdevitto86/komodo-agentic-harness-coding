package intake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// PlanDraft is the planner's result: the plan.md text and its parsed blueprint, in memory.
type PlanDraft struct {
	Files     []DraftFile `json:"files"`
	Blueprint Blueprint   `json:"-"` // plan.md parsed; empty when the draft was built from files alone
}

// DraftFile is one file of a draft.
type DraftFile struct {
	Path    string `json:"path"`    // path in the draft, such as plan.md
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
	Plan   string            // the plan id
	Path   string            // absolute path of plan.md
	Groups []string          // labels of the groups in the plan, in dependency order
	Hashes map[string]string // card hash of each group, by label
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

// writeError marks a failure to write the plan folder.
type writeError struct{ err error }

// Error returns the wrapped failure.
func (e writeError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped failure.
func (e writeError) Unwrap() error { return e.err }

// LintPlan lints a draft for the planner's lint_plan tool, against the tracked files of the plan in progress.
func (h *Handler) LintPlan(d PlanDraft) []Finding {
	var (
		tracked  []string
		existing Blueprint
	)

	h.stateMu.Lock()
	if h.current != nil {
		tracked, existing = h.current.tracked, h.current.existing()
	}

	h.stateMu.Unlock()

	return LintDraft(d, existing, h.protected(), tracked)
}

// Plan runs the planner session, lints its draft and writes the plan folder only when lint passes.
func (h *Handler) Plan(ctx context.Context, r PlanRequest) (PlanResult, error) {
	// noGoalText stands in for the goal slot when a request names only documents.
	const noGoalText = "Plan the work described in the source documents."

	h.mu.Lock()
	defer h.mu.Unlock()

	if strings.TrimSpace(r.Goal) == "" && len(r.Docs) == 0 {
		return PlanResult{}, errors.New("a plan needs a goal, a document or both")
	}
	if r.Plan == "" && r.Group != "" {
		return PlanResult{}, errors.New("a group rewrite needs the plan id of the group")
	}
	if r.Plan != "" && strings.TrimSpace(r.Goal) == "" {
		return PlanResult{}, errors.New("a change to a plan needs a prompt")
	}

	tree, err := h.d.Tree(ctx)
	if err != nil {
		return PlanResult{}, fmt.Errorf("read the base commit: %w", err)
	}

	specs, err := readDocs(tree, r.Docs)
	if err != nil {
		return PlanResult{}, err
	}
	if err := restoreOrphans(h.d.Home, h.d.Repo); err != nil {
		return PlanResult{}, writeError{fmt.Errorf("restore interrupted plan: %w", err)}
	}

	pl := &planState{tracked: tree.Paths(), group: r.Group}
	slots := contextmgr.Values{contextmgr.Specs: specs}

	if r.Plan != "" {
		stored, err := h.loadStored(r)
		if err != nil {
			return PlanResult{}, err
		}

		pl.stored, slots[contextmgr.Plan] = &stored.blueprint, stored.text

		if r.Group != "" {
			if pl.finished, err = h.finishedTasks(stored.blueprint, r.Group); err != nil {
				return PlanResult{}, err
			}
		}
	}

	h.stateMu.Lock()
	h.current = pl

	h.stateMu.Unlock()
	defer func() { h.stateMu.Lock(); h.current = nil; h.stateMu.Unlock() }()

	if h.d.Limits.WallMin > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(h.d.Limits.WallMin)*h.wallUnit)
		defer cancel()
	}

	slots[contextmgr.Goal] = strings.TrimSpace(r.Goal)
	if slots[contextmgr.Goal] == "" {
		slots[contextmgr.Goal] = noGoalText
	}
	if r.Group != "" {
		slots[contextmgr.Goal] = fmt.Sprintf("Rewrite only the tasks of group %s: %s", r.Group, slots[contextmgr.Goal])
	}
	if len(pl.finished) > 0 {
		slots[contextmgr.Goal] += "\nThese tasks of the group are finished and fixed; keep each exactly as written: " +
			strings.Join(pl.finished, ", ")
	}

	c, err := h.d.Context.GetContext(ctx, contextmgr.Request{Agent: "planner", Tree: tree, Chain: h.d.Chain, Slots: slots})
	if err != nil {
		return PlanResult{}, fmt.Errorf("build the planner brief: %w", err)
	}

	sess, err := h.d.Start(ctx)
	if err != nil {
		return PlanResult{}, h.sessionError(ctx, err)
	}

	defer func() { _ = sess.Close() }()

	return h.lintLoop(ctx, sess, c.Brief.Text(), pl)
}

// lintLoop sends the brief, lints each draft and sends failures back until a draft passes or the third fails.
func (h *Handler) lintLoop(ctx context.Context, sess PlannerSession, brief string, pl *planState) (PlanResult, error) {
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

		found := sortFindings(LintDraft(draft, pl.existing(), protected, pl.tracked))
		if len(failing(found)) == 0 {
			held, err := h.amendCheck(draft, pl)
			if err != nil {
				return PlanResult{}, err
			}

			found = sortFindings(append(found, held...))
		}
		if len(failing(found)) == 0 {
			more, err := base.findings(ctx, pl.scope(draft.blueprint()))
			if err != nil && ctx.Err() != nil {
				return PlanResult{}, h.sessionError(ctx, err)
			}
			if err != nil {
				return PlanResult{}, err
			}

			found = append(found, more...)
		}
		if len(failing(found)) == 0 {
			return h.write(draft, pl)
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

// DraftFromResult wraps the planner's plan text as a draft holding plan.md and the blueprint parsed from it. Grammar
// problems are not dropped here; lint finds them in the text.
func DraftFromResult(r agent.PlannerResult) PlanDraft {
	b, _ := Parse(r.Plan)
	return PlanDraft{Files: []DraftFile{{Path: "plan.md", Content: r.Plan}}, Blueprint: b}
}

// blueprint returns the draft's parsed blueprint, parsing plan.md when the draft carries none.
func (d PlanDraft) blueprint() Blueprint {
	if d.Blueprint.Goal != "" || len(d.Blueprint.Groups) > 0 {
		return d.Blueprint
	}

	i := slices.IndexFunc(d.Files, func(f DraftFile) bool { return f.Path == "plan.md" })
	if i < 0 {
		return Blueprint{}
	}

	b, _ := Parse(d.Files[i].Content)

	return b
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

// failing returns the failing findings.
func failing(found []Finding) []Finding {
	var out []Finding
	for _, f := range found {
		if f.Severity == SeverityFail {
			out = append(out, f)
		}
	}
	return out
}
