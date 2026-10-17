package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// scopeAnswerer is implemented by a builder role that keeps the text of a scope answer for a fresh
// brief.
type scopeAnswerer interface {
	SetScopeAnswer(s string)
}

// branch is the end of one judge's work in Test and Review.
type branch struct {
	out sessOut
	err error
}

// roundResult is the decision of one Test and Review round.
type roundResult struct {
	findings []string
	again    bool
}

// pendingTasks lists the tasks whose completion command has not passed yet.
func (m *Manager) pendingTasks() []TaskSpec {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []TaskSpec

	for _, t := range m.spec.Tasks {
		if !slices.Contains(m.prog.TasksDone, t.ID) {
			out = append(out, t)
		}
	}

	return out
}

// noteFailure charges a failed builder session to the group and escalates when the attempts are
// spent.
func (m *Manager) noteFailure(ctx context.Context) error {
	m.mu.Lock()
	m.prog.Attempts++
	n := m.prog.Attempts
	m.mu.Unlock()

	if n >= m.spec.Limits.Attempts {
		return block("attempts", map[string]int{"attempts": n})
	}

	return m.report(ctx)
}

// runBuild runs builder sessions until every task of the group has passed its completion command.
func (m *Manager) runBuild(ctx context.Context) error {
	if err := m.setStage(ctx, state.StageBuild); err != nil {
		return err
	}

	if _, err := m.instance(ctx, "builder"); err != nil {
		return err
	}

	m.mu.Lock()
	m.verdicts = map[string]Outcome{}
	findings, input := m.pending, ""
	m.pending = nil

	var resume *llm.ResumeHandle

	switch h, saved := m.prog.Handles["builder"]; {
	case m.scopeInput != "" && m.builderH != nil:
		hh := *m.builderH
		resume, input, m.scopeInput = &hh, m.scopeInput, ""
	case saved:
		resume, input = &h, "Continue where you stopped."
	case len(findings) > 0 && m.builderH != nil:
		hh := *m.builderH
		resume, input = &hh, "The review sent the work back with these findings:\n"+bullets(
			findings,
		)
	}
	m.mu.Unlock()

	var timeouts, crashes, refusals int

	for {
		if err := m.checkpoint(ctx); err != nil {
			return err
		}

		if err := m.checkStop(ctx); err != nil {
			return err
		}

		in := RoleInput{Tasks: m.pendingTasks(), Findings: findings}
		if m.Progress().Returns > 0 && m.c.Git != nil {
			in.Diff, _ = m.c.Git.Diff(ctx, m.spec.Lease, nil)
		}

		task := ""
		if p := m.pendingTasks(); len(p) > 0 {
			task = p[0].ID
		}

		out, err := m.runAgent(
			ctx,
			sessReq{agent: "builder", task: task, in: in, resume: resume, input: input},
		)
		if err != nil {
			return err
		}

		resume, input = nil, ""

		switch out.reason {
		case state.ReasonDone:
			switch out.outcome.Kind {
			case OutcomeScope:
				h, text, err := m.handleScope(ctx, out)
				if err != nil {
					return err
				}

				resume, input = h, text

				continue
			case OutcomePass:
			default:
				return block(
					"contract",
					map[string]string{"error": "the builder's result is not a pass"},
				)
			}

			left, err := m.reconcile(ctx)
			if err != nil {
				return err
			}

			if len(left) > 0 {
				findings = left

				if err := m.noteFailure(ctx); err != nil {
					return err
				}

				continue
			}

			return m.setStage(ctx, state.StageTestReview)
		case state.ReasonTimeout, state.ReasonIdle:
			if timeouts++; timeouts > m.spec.Limits.SessionRetries {
				return block("retries", map[string]string{"last": string(out.reason)})
			}

			if err := m.noteFailure(ctx); err != nil {
				return err
			}
		case state.ReasonCheckFail, state.ReasonSchemaFail, state.ReasonNoResult:
			if err := m.noteFailure(ctx); err != nil {
				return err
			}
		case state.ReasonRefusals:
			if refusals++; refusals > m.spec.Limits.SessionRetries {
				return block("refusals", map[string]any{"row": out.refusedRow(m)})
			}

			if err := m.noteFailure(ctx); err != nil {
				return err
			}
		case state.ReasonCrash:
			if crashes++; crashes > max(m.spec.Limits.SessionRetries, 1) {
				return &blockErr{kind: EscFailed, reason: "crash"}
			}
		case state.ReasonBilling:
			return block("paid_refused", map[string]string{"setting": "providers.paid"})
		case state.ReasonCancelled:
			return ErrStopped
		}
	}
}

// taskDone is the builder's task_done tool: it reruns the task's completion command and ends the
// session after the third failure.
func (m *Manager) taskDone(ctx context.Context, sc *sessCtx, task string) (int, string, error) {
	// gateFailLimit is the number of failed completion commands of one task that ends the builder's
	// session.
	const gateFailLimit = 3

	exit, out, err := m.finishTask(ctx, task)
	if err != nil {
		return exit, out, err
	}

	m.mu.Lock()
	if exit == 0 {
		delete(m.gateFails, task)
	} else {
		m.gateFails[task]++
	}

	n := m.gateFails[task]
	m.mu.Unlock()

	if n >= gateFailLimit && sc.watch != nil {
		sc.watch.stop(state.ReasonCheckFail)
	}

	return exit, out, nil
}

// finishTask reruns a task's completion command; on exit 0 it marks the task done and asks the
// Commit Handler for a local commit.
func (m *Manager) finishTask(ctx context.Context, id string) (int, string, error) {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	var task *TaskSpec

	for i := range m.spec.Tasks {
		if m.spec.Tasks[i].ID == id {
			task = &m.spec.Tasks[i]
		}
	}

	if task == nil {
		return -1, "", fmt.Errorf("the task %q is not in this group", id)
	}

	m.mu.Lock()
	done := slices.Contains(m.prog.TasksDone, id)
	m.mu.Unlock()

	if done {
		return 0, "the task already passed", nil
	}

	o, err := agent.TaskGate{
		Run: m.runner("builder"),
	}.Complete(
		ctx,
		agent.TaskRef{Group: m.spec.Group, Task: id, DoneCommand: task.DoneWhen},
	)
	if err != nil {
		return -1, "", err
	}

	if o.ExitCode != 0 {
		return o.ExitCode, o.Output, nil
	}

	if m.c.Commit != nil {
		_, err := m.c.Commit.Save(
			ctx,
			commit.SaveRequest{
				Run:      bus.ID(m.spec.Run),
				Group:    bus.ID(m.spec.Group),
				Repo:     m.c.Meta.Repo,
				Label:    m.spec.Group,
				Task:     id,
				Worktree: m.spec.Lease.Worktree,
				Branch:   m.spec.Lease.Branch,
				Fence:    lease.Fence(m.spec.Lease.Fence),
			},
		)
		if err != nil {
			return -1, "", fmt.Errorf("save the task %s: %w", id, err)
		}
	}

	m.mu.Lock()
	m.prog.TasksDone = append(m.prog.TasksDone, id)
	m.mu.Unlock()
	m.fact("task.done", map[string]string{"task": id})

	if err := m.report(ctx); err != nil {
		return 0, o.Output, err
	}

	return 0, o.Output, nil
}

// reconcile runs the completion command of every task not yet done and returns the findings for
// those that still fail.
func (m *Manager) reconcile(ctx context.Context) ([]string, error) {
	var left []string

	for _, t := range m.pendingTasks() {
		exit, out, err := m.finishTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}

		if exit != 0 {
			left = append(
				left,
				fmt.Sprintf(
					"task %s is not done: its completion command exited %d\n%s",
					t.ID,
					exit,
					out,
				),
			)
		}
	}

	return left, nil
}

// scopeGuard returns a guard for scope decisions: the rows that protect paths, built for the
// builder's permissions.
func (m *Manager) scopeGuard() (*Guard, error) {
	home, _ := os.UserHomeDir()

	rows, err := LoadRows(home)
	if err != nil {
		return nil, err
	}

	return NewGuard(
		rows,
		Binding{
			Agent:       "builder",
			Permissions: m.spec.Agents["builder"],
			Worktree:    m.spec.Lease.Worktree,
			Files:       m.effectiveFiles(),
		},
		m.spec.Limits.RefusalLimit,
	)
}

// scopeAllowed returns the paths the Task Manager would itself allow: not protected, not secret,
// not another group's.
func (m *Manager) scopeAllowed(paths []string) ([]string, error) {
	g, err := m.scopeGuard()
	if err != nil {
		return nil, err
	}

	protected := func(p string) bool {
		_, bad := g.ScopeDenied(p)

		return bad
	}
	other := func(p string) (string, bool) {
		for _, glob := range m.spec.OtherFiles {
			if agent.MatchPath(glob, p) {
				return glob, true
			}
		}

		return "", false
	}

	var keep []string

	for _, p := range paths {
		if _, _, denied := agent.AutoDeny([]string{p}, protected, other); !denied {
			keep = append(keep, p)
		}
	}

	return keep, nil
}

// handleScope answers a builder's needs_scope result: it denies at once or escalates the request.
func (m *Manager) handleScope(ctx context.Context, out sessOut) (*llm.ResumeHandle, string, error) {
	task := ""
	if p := m.pendingTasks(); len(p) > 0 {
		task = p[0].ID
	}

	m.fact("scope.request", map[string]any{"task": task, "paths": out.outcome.Paths})
	m.mu.Lock()

	asked := slices.Contains(m.prog.ScopeAsked, task)
	if !asked {
		m.prog.ScopeAsked = append(m.prog.ScopeAsked, task)
	}
	m.mu.Unlock()

	h := out.handle

	deny := func(why string) (*llm.ResumeHandle, string, error) {
		m.fact("scope.answer", map[string]any{"action": "deny", "auto": true})

		text := "scope request denied: " + why
		if r, ok := m.c.Roles["builder"].(scopeAnswerer); ok {
			r.SetScopeAnswer(text)
		}

		return &h, text, m.report(ctx)
	}
	if asked {
		return deny("one scope request per task")
	}

	keep, err := m.scopeAllowed(out.outcome.Paths)
	if err != nil {
		return nil, "", err
	}

	if len(keep) == 0 {
		return deny("every path is protected, secret or belongs to another group")
	}

	m.mu.Lock()
	m.scopeWait = &scopeWait{task: task, paths: keep}
	m.builderH = &h
	m.mu.Unlock()

	return nil, "", &blockErr{
		kind:   EscScope,
		reason: "needs_scope",
		detail: map[string]any{"task": task, "paths": keep, "reason": out.outcome.Reason},
	}
}

// applyScopeAnswer widens the group's files by the approved paths the Task Manager would itself
// allow.
func (m *Manager) applyScopeAnswer(ctx context.Context, a Answer) error {
	m.mu.Lock()
	m.scopeWait = nil
	m.mu.Unlock()

	text := "scope request denied"

	if a.Action == "approve" {
		keep, err := m.scopeAllowed(a.Approved)
		if err != nil {
			return err
		}

		m.mu.Lock()
		for _, p := range keep {
			if !slices.Contains(m.files, p) {
				m.files = append(m.files, p)
				m.prog.Widened = append(m.prog.Widened, p)
			}
		}

		files := slices.Clone(m.files)
		for _, g := range m.guards {
			g.SetFiles(files)
		}
		m.mu.Unlock()

		text = "scope request approved for: " + strings.Join(keep, ", ")
	}

	if r, ok := m.c.Roles["builder"].(scopeAnswerer); ok {
		r.SetScopeAnswer(text)
	}

	m.mu.Lock()
	m.scopeInput = text
	m.mu.Unlock()

	return m.report(ctx)
}

// pauseFor holds the group because every link is limited or a resume was refused; the Runtime
// Manager sleeps and calls Resume.
func (m *Manager) pauseFor(
	ctx context.Context,
	limits []*llm.ProviderLimit,
	agentName string,
	handle *llm.ResumeHandle,
	refused bool,
) error {
	const pauseGrace = time.Minute

	var (
		latest time.Time
		link   llm.Link
	)

	for _, l := range limits {
		if l.Handle != nil && handle == nil {
			handle = l.Handle
		}

		if m.c.Router != nil && !refused {
			_ = m.c.Router.RecordLimit(ctx, l)
		}

		if l.ResetAt.After(latest) || link == "" {
			link = l.Link
		}

		if l.ResetAt.After(latest) {
			latest = l.ResetAt
		}
	}

	m.mu.Lock()

	p := &PauseState{
		Since:          m.lastPause.Since,
		RefusedResumes: m.lastPause.RefusedResumes,
		WaitLink:       string(link),
	}
	if cur := m.prog.Pause; cur != nil {
		p.Since, p.RefusedResumes, p.Owed = cur.Since, cur.RefusedResumes, slices.Clone(cur.Owed)
	}

	if p.Since.IsZero() {
		p.Since = m.now()
	}

	if refused {
		p.RefusedResumes++
	}

	if !slices.Contains(p.Owed, agentName) {
		p.Owed = append(p.Owed, agentName)
	}

	switch {
	case !latest.IsZero():
		p.WaitUntil = latest.Add(pauseGrace)
	case refused:
		p.WaitUntil = m.now().Add(m.resumeWait(p.RefusedResumes))
	default:
		p.WaitUntil = m.now().Add(unknownReset)
	}

	if handle != nil {
		hh := *handle
		p.Handle = &hh

		if m.prog.Handles == nil {
			m.prog.Handles = map[string]llm.ResumeHandle{}
		}

		m.prog.Handles[agentName] = hh
	}

	m.prog.Pause = p
	m.mu.Unlock()
	m.fact(
		"group.pause",
		map[string]any{"link": link, "wait_until": p.WaitUntil, "agent": agentName},
	)

	if err := m.report(ctx); err != nil {
		return err
	}

	return ErrGroupPaused
}

// resumeWait is the wait after the nth refused resume: 15, 30, then 60 minutes.
func (m *Manager) resumeWait(try int) time.Duration {
	if m.c.ResumeWait != nil {
		return m.c.ResumeWait(try)
	}

	switch {
	case try <= 1:
		return 15 * time.Minute
	case try == 2:
		return 30 * time.Minute
	}

	return time.Hour
}

// judgeBranch runs one judge to a decided outcome; a disputed tester result restarts the tester
// once.
func (m *Manager) judgeBranch(ctx context.Context, name string, in RoleInput) branch {
	var retries int

	for {
		if err := m.checkpoint(ctx); err != nil {
			return branch{err: err}
		}

		out, err := m.runAgent(ctx, sessReq{agent: name, in: in})
		if err != nil {
			return branch{err: err}
		}

		switch out.reason {
		case state.ReasonDone:
			if out.outcome.Kind != OutcomeRestart {
				return branch{out: out}
			}

			m.mu.Lock()
			d := m.prog.Disputes
			m.prog.Disputes = 1
			m.mu.Unlock()

			if d >= 1 {
				out.outcome = Outcome{
					Kind:     OutcomeBlock,
					Reason:   agent.BlockTester,
					Findings: out.outcome.Findings,
				}

				return branch{out: out}
			}

			in.Findings = out.outcome.Findings

			if err := m.report(ctx); err != nil {
				return branch{err: err}
			}
		case state.ReasonCancelled:
			return branch{out: out}
		case state.ReasonBilling:
			return branch{
				err: block("paid_refused", map[string]string{"setting": "providers.paid"}),
			}
		default:
			if retries++; retries > m.spec.Limits.SessionRetries {
				return branch{
					err: block(
						"retries",
						map[string]string{"agent": name, "last": string(out.reason)},
					),
				}
			}
		}
	}
}

// dirtyWork reports the git status of the worktree.
func (m *Manager) dirtyWork(ctx context.Context) (string, error) {
	if m.c.Git == nil {
		return "", nil
	}

	return m.c.Git.ReadOnly(ctx, m.spec.Lease, "status", []string{"--porcelain"})
}

// runTestReview runs the free checks, then the tester and the reviewer side by side, and decides
// the round.
func (m *Manager) runTestReview(ctx context.Context) error {
	if err := m.setStage(ctx, state.StageTestReview); err != nil {
		return err
	}

	if _, err := m.instance(ctx, "tester"); err != nil {
		return err
	}

	m.mu.Lock()

	var owed []string

	for _, a := range m.lastPause.Owed {
		if a == "tester" || a == "reviewer" {
			owed = append(owed, a)
		}
	}

	m.lastPause.Owed = nil
	m.mu.Unlock()

	if len(owed) == 0 {
		if err := m.saveDirty(ctx); err != nil {
			return err
		}

		if fails, err := m.runChecks(ctx); err != nil {
			return err
		} else if len(fails) > 0 {
			return m.rework(ctx, fails)
		}
	}

	for {
		round, err := m.round(ctx, owed)
		owed = nil

		if err != nil {
			return err
		}

		if round.again {
			continue
		}

		if len(round.findings) > 0 {
			return m.rework(ctx, round.findings)
		}

		return m.setStage(ctx, state.StageCommit)
	}
}

// saveDirty asks the Commit Handler to save work the builder left uncommitted, so a reset after the
// judges cannot lose it.
func (m *Manager) saveDirty(ctx context.Context) error {
	st, err := m.dirtyWork(ctx)
	if err != nil {
		return err
	}

	if strings.TrimSpace(st) == "" || m.c.Commit == nil || len(m.spec.Tasks) == 0 {
		return nil
	}

	_, err = m.c.Commit.Save(
		ctx,
		commit.SaveRequest{
			Run:      bus.ID(m.spec.Run),
			Group:    bus.ID(m.spec.Group),
			Repo:     m.c.Meta.Repo,
			Label:    m.spec.Group,
			Task:     m.spec.Tasks[len(m.spec.Tasks)-1].ID,
			Worktree: m.spec.Lease.Worktree,
			Branch:   m.spec.Lease.Branch,
			Fence:    lease.Fence(m.spec.Lease.Fence),
		},
	)
	if err != nil {
		return fmt.Errorf("save the builder's last edits: %w", err)
	}

	return nil
}

// runChecks runs the mechanical checks in the tester's sandbox and returns the failures, each with
// its exact output.
func (m *Manager) runChecks(ctx context.Context) ([]string, error) {
	if m.c.Checks == nil {
		return nil, nil
	}

	tree, err := m.c.Git.TreeHash(ctx, m.spec.Lease)
	if err != nil {
		return nil, err
	}

	diff, err := m.c.Git.Diff(ctx, m.spec.Lease, nil)
	if err != nil {
		return nil, err
	}

	done := map[string]string{}
	for _, t := range m.spec.Tasks {
		done[t.ID] = t.DoneWhen
	}

	res, err := m.c.Checks.Check(
		ctx,
		m.runner("tester"),
		agent.CheckInput{
			Group:    m.spec.Group,
			Base:     m.c.Base,
			Head:     tree,
			Files:    m.effectiveFiles(),
			Changed:  diffPaths(diff),
			DoneWhen: done,
			Language: m.c.Language,
		},
	)
	if err != nil {
		return nil, block(
			"setup",
			map[string]string{"error": err.Error(), "step": "mechanical checks"},
		)
	}

	m.fact("check.done", res.Done(tree))

	if res.Clean() {
		return nil, nil
	}

	return failedChecks(res), nil
}

// round runs the judges that are due, then decides: pass, return with findings, block, or run again
// after a tracked edit.
func (m *Manager) round(ctx context.Context, owed []string) (roundResult, error) {
	diff := ""
	if m.c.Git != nil {
		diff, _ = m.c.Git.Diff(ctx, m.spec.Lease, nil)
	}

	in := RoleInput{Diff: diff, Trip: m.Progress().Returns}
	judges := []string{"tester", "reviewer"}
	res := map[string]*branch{}

	tctx, tcancel := context.WithCancel(ctx)
	defer tcancel()

	var wg sync.WaitGroup

	for _, name := range judges {
		if v, ok := m.verdictOf(name); ok && len(owed) > 0 && !slices.Contains(owed, name) {
			res[name] = &branch{out: sessOut{reason: state.ReasonDone, outcome: v}}

			continue
		}

		b := &branch{}
		res[name] = b

		wg.Add(1)

		go func() {
			defer guardian.Recover(taskSource, nil)

			finished := false

			defer wg.Done()
			defer func() {
				if !finished {
					*b = branch{err: fmt.Errorf("the %s goroutine crashed", name)}
				}
			}()

			c := tctx
			if name == "reviewer" {
				c = ctx
			}

			*b = m.judgeBranch(c, name, in)
			if name == "tester" && b.err != nil && tctx.Err() != nil && ctx.Err() == nil &&
				(errors.Is(b.err, context.Canceled) || errors.Is(b.err, ErrStopped)) {
				*b = branch{out: sessOut{reason: state.ReasonCancelled}}
			}

			if name == "reviewer" && b.err == nil && b.out.outcome.Kind == OutcomeReturn {
				tcancel()
			}

			finished = true
		}()
	}

	wg.Wait()

	if err := m.checkStop(ctx); err != nil {
		return roundResult{}, err
	}

	var (
		paused, other error
		blocked       *blockErr
	)

	for _, name := range judges {
		err := res[name].err
		if err == nil && res[name].out.reason == "" {
			err = fmt.Errorf("the %s ended with no exit reason", name)
		}

		var be *blockErr
		switch {
		case err == nil:
		case errors.Is(err, ErrGroupPaused):
			paused = err
		case errors.As(err, &be) && blocked == nil:
			blocked = be
		case !errors.As(err, &be):
			other = err
		}

		if err == nil && res[name].out.reason == state.ReasonDone &&
			res[name].out.outcome.Kind == OutcomePass {
			m.mu.Lock()
			m.verdicts[name] = res[name].out.outcome
			m.mu.Unlock()
		}
	}

	switch {
	case paused != nil:
		return roundResult{}, paused
	case blocked != nil:
		return roundResult{}, blocked
	case other != nil:
		return roundResult{}, other
	}

	if again, err := m.afterJudges(ctx); err != nil || again {
		return roundResult{again: again}, err
	}

	var findings []string

	for _, name := range judges {
		b := res[name]
		if b.out.reason == state.ReasonCancelled {
			continue
		}

		switch b.out.outcome.Kind {
		case OutcomeBlock:
			return roundResult{}, block(
				b.out.outcome.Reason,
				map[string]any{"agent": name, "findings": b.out.outcome.Findings},
			)
		case OutcomeReturn:
			findings = append(findings, b.out.outcome.Findings...)
		}
	}

	return roundResult{findings: findings}, nil
}

// verdictOf returns a judge's stored passing outcome.
func (m *Manager) verdictOf(name string) (Outcome, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.verdicts[name]

	return v, ok
}

// afterJudges resets what the judges' commands left in the worktree; a tracked edit discards the
// round, and a second one blocks.
func (m *Manager) afterJudges(ctx context.Context) (bool, error) {
	if m.c.Git == nil {
		return false, nil
	}

	st, err := m.dirtyWork(ctx)
	if err != nil {
		return false, err
	}

	edits := trackedEdits(st)

	if err := m.c.Git.ResetTracked(ctx, m.spec.Lease); err != nil {
		return false, fmt.Errorf("reset the worktree after the judges: %w", err)
	}

	if len(edits) == 0 {
		return false, nil
	}

	m.mu.Lock()
	m.contract++
	n := m.contract
	m.verdicts = map[string]Outcome{}
	m.mu.Unlock()

	if n >= 2 {
		return false, block("contract", map[string]any{"edited": edits})
	}

	m.fact("judge.reset", map[string]any{"edited": edits})

	return true, nil
}

// rework sends the group back to Build with findings, inside the bounds on returns and unchanged
// trees.
func (m *Manager) rework(ctx context.Context, findings []string) error {
	m.mu.Lock()
	returns := m.prog.Returns
	last := m.prog.TreeAtReturn
	m.mu.Unlock()

	if returns >= m.spec.Limits.Returns {
		return block("returns", map[string]any{"returns": returns, "findings": findings})
	}

	tree, err := m.c.Git.TreeHash(ctx, m.spec.Lease)
	if err != nil {
		return err
	}

	if tree == last {
		return block("flap", map[string]any{"tree": tree, "findings": findings})
	}

	m.mu.Lock()
	m.prog.TreeAtReturn, m.prog.Returns, m.prog.Disputes = tree, returns+1, 0
	m.pending, m.contract = findings, 0
	m.verdicts = map[string]Outcome{}
	m.mu.Unlock()

	return m.setStage(ctx, state.StageBuild)
}

// runCommit hands the approved group to the Commit Handler and enters cleanup.
func (m *Manager) runCommit(ctx context.Context) error {
	if err := m.setStage(ctx, state.StageCommit); err != nil {
		return err
	}

	tree, err := m.c.Git.TreeHash(ctx, m.spec.Lease)
	if err != nil {
		return err
	}

	t, _ := m.verdictOf("tester")
	r, _ := m.verdictOf("reviewer")

	a := commit.Approval{
		Run:        bus.ID(m.spec.Run),
		Group:      bus.ID(m.spec.Group),
		Repo:       m.c.Meta.Repo,
		Epic:       m.c.Meta.Epic,
		Label:      m.spec.Group,
		EpicGroups: m.c.Meta.EpicGroups,
		Goal:       m.c.Meta.Goal,
		Worktree:   m.spec.Lease.Worktree,
		Branch:     m.spec.Lease.Branch,
		Fence: lease.Fence(
			m.spec.Lease.Fence,
		),
		Tree:   tree,
		Tester: commit.Verdict{Tree: tree, Summary: t.Summary},
		Reviewer: commit.Verdict{
			Tree:    tree,
			Summary: r.Summary,
		},
		Facts: commit.PRFacts{TasksClosed: m.Progress().TasksDone},
	}
	if _, err := m.c.Commit.Publish(ctx, a); err != nil {
		return block("publish", map[string]string{"error": err.Error()})
	}

	return m.setStage(ctx, state.StageCleanup)
}

// refusedRow names the guard row with the most refusals in the session.
func (o sessOut) refusedRow(m *Manager) string {
	best, row := 0, ""
	for k, v := range m.Progress().Refusals {
		if v > best {
			best, row = v, k
		}
	}

	return row
}

// failedChecks renders the steps of a check result that failed, with their exact output.
func failedChecks(r agent.CheckResult) []string {
	var out []string

	for _, s := range r.Steps {
		if s.Level != agent.LevelFail {
			continue
		}

		var b strings.Builder
		fmt.Fprintf(&b, "the %s check failed", s.Step)

		for _, d := range s.Detail {
			b.WriteString("\n  " + d)
		}

		if s.Output != "" {
			b.WriteString("\n" + s.Output)
		}

		out = append(out, b.String())
	}

	return out
}
