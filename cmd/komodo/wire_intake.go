package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// intakeHolder lets the planner's tool host reach the Intake Handler built from the same dependencies.
type intakeHolder struct {
	k *repoKit
}

// start opens a planner session over the first link of the planner's chain that can serve it.
func (h *intakeHolder) start(ctx context.Context) (_ intake.PlannerSession, err error) {
	s, k := h.k.s, h.k
	ag, ok := s.cfg.GetAgent("planner")
	if !ok {
		return nil, errors.New("no planner in the agent registry")
	}

	router := &task.Router{
		Client:   s.llm,
		Quota:    limits{s.machine},
		Paid:     s.str("providers.paid", "never"),
		CtxFloor: int(s.num("providers.ctx_floor")),
		Catalog:  s.catalog,
	}

	chain := make([]llm.Link, len(ag.Chain))
	for i, c := range ag.Chain {
		chain[i] = llm.Link(c)
	}

	sel, err := router.Select(ctx, task.SelectRequest{Chain: chain, Weight: llm.Weight(ag.Weight)})
	if err != nil {
		return nil, fmt.Errorf("select a link for the planner: %w", err)
	}

	id := newSessionID()
	dir := filepath.Join(s.home, "sessions", "planner-"+id)
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		return nil, fmt.Errorf("make the planner session folder: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	uh, _ := os.UserHomeDir()
	rows, err := task.LoadRows(uh)
	if err != nil {
		return nil, fmt.Errorf("load the guard rows: %w", err)
	}

	g, err := task.NewGuard(rows, task.Binding{Agent: "planner", Permissions: permissionsOf(ag), Worktree: k.root}, 3)
	if err != nil {
		return nil, fmt.Errorf("build the planner guard: %w", err)
	}

	env := &task.Env{Worktree: k.root, Dir: dir, Lint: h.lint}
	b, err := task.NewRegistry(env).Bind(ag.Tools, g)
	if err != nil {
		return nil, fmt.Errorf("bind the planner tools: %w", err)
	}

	sess := llm.Session{
		ID:      id,
		Dir:     dir,
		Agent:   "planner",
		Link:    sel.Link,
		Model:   sel.Model,
		Profile: sel.Profile,
		Schema:  ag.Result,
	}
	ps := &plannerSession{c: s.llm, sess: sess}

	if !task.ProgramLink(sel.Link) {
		ps.sess.Tools = b
		return ps, nil
	}

	exe, _ := os.Executable()
	cfg := task.ToolServerConfig{Agent: "planner", Session: id, Socket: filepath.Join(s.home, "state", engine.SocketName)}

	if err := task.WriteMCPConfig(dir, exe, cfg); err != nil {
		return nil, err
	}

	ps.closers = append(ps.closers, s.tools.Add(id, b))

	return ps, nil
}

// lint runs the planner's lint_backlog tool on a draft given as text.
func (h *intakeHolder) lint(draft string) []string {
	var d intake.PlanDraft
	if err := json.Unmarshal([]byte(draft), &d); err != nil {
		return []string{"the draft is not a JSON task list: " + err.Error()}
	}

	var out []string
	for _, f := range h.k.intake.LintBacklog(d) {
		out = append(out, fmt.Sprint(f))
	}

	return out
}

// plannerSession is one planner conversation: the first send starts the session, later sends resume it.
type plannerSession struct {
	c       *llm.Client
	sess    llm.Session
	started bool
	closers []func() // end the session's tool route
}

// Send runs one planner turn and returns the draft it filed; the first text is the brief and is written to the folder.
func (p *plannerSession) Send(ctx context.Context, text string) (intake.PlanDraft, error) {
	var (
		st  llm.Stream
		err error
	)

	if !p.started {
		p.started = true
		if err = task.WriteBriefFiles(p.sess.Dir, [5]string{4: text}); err == nil {
			st, err = p.c.Start(ctx, p.sess)
		}
	} else {
		st, err = p.c.Resume(ctx, p.sess, llm.Input{Text: text})
	}

	if err != nil {
		return intake.PlanDraft{}, fmt.Errorf("run the planner: %w", err)
	}

	defer func() { _ = st.Close() }()

	for {
		ev, err := st.Next(ctx)
		if errors.Is(err, io.EOF) {
			return intake.PlanDraft{}, errors.New("the planner ended without a result")
		}
		if err != nil {
			return intake.PlanDraft{}, fmt.Errorf("read the planner: %w", err)
		}
		if ev.Result != nil {
			pr, err := agent.ParsePlannerResult(ev.Result.Output)
			if err != nil {
				return intake.PlanDraft{}, err
			}
			return intake.DraftFromResult(pr), nil
		}
	}
}

// Close ends the session's tool route.
func (p *plannerSession) Close() error {
	for _, c := range p.closers {
		c()
	}

	p.closers = nil

	return nil
}

// intakeDeps wires the Intake Handler to the repository the engine serves.
func (k *repoKit) intakeDeps(cm *contextmgr.Manager) intake.Deps {
	s := k.s
	h := &intakeHolder{k: k}
	ag, _ := s.cfg.GetAgent("planner")
	d := intake.Deps{
		Bus: s.bus, Context: cm, Start: h.start, Locks: groupLocks{k.st},
		Protected: protectedPaths,
		Limits:    intake.PlannerLimits{WallMin: ag.Limits.WallMin, IdleMin: ag.Limits.IdleMin},
		Root:      k.root, Tree: func(context.Context) (contextmgr.Tree, error) {
			return task.NewBaseTree(gitRunner{k.root}, "HEAD"), nil
		},
		Repo: k.id, CommonDir: k.common,
	}

	return d
}

// newSessionID returns a random session identifier in the UUID shape the claude program requires.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])

	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// protectedPaths lists the paths no agent may write: the guard policy's protected-path and secret-file rows.
func protectedPaths() []string {
	uh, _ := os.UserHomeDir()
	rows, err := task.LoadRows(uh)
	if err != nil {
		return nil
	}

	var out []string
	for _, r := range rows {
		if r.ID == "G3" || r.ID == "G4" {
			out = append(out, r.Pattern...)
		}
	}

	return out
}
