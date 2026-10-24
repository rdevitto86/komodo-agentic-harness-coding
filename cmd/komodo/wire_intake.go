package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
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

// lint runs the planner's lint_plan tool on a draft given as text.
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
		OpenBase: k.openBase,
	}

	return d
}

// baseRunner runs each shell command on its own fresh checkout of the base commit, in its own builder sandbox instance.
type baseRunner struct {
	boxes  *sandbox.Manager
	root   string
	base   string
	group  string
	plan   task.SandboxPlan
	folder string // holds every checkout, temporary folder and the build cache folder of this plan; Close deletes it
	build  string // the check's own build cache folder inside folder; never the shared one with Quality Assurance's checkout
	runs   int
}

// openBaseRunner reads the repository's HEAD commit and makes the plan's folder; each command then gets its own checkout and
// a builder sandbox with no network over it.
func openBaseRunner(
	ctx context.Context,
	boxes *sandbox.Manager,
	root, group string,
	plan task.SandboxPlan,
) (intake.BaseRunner, error) {
	base, err := gitOut("-C", root, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read the base commit: %w", err)
	}
	if err := os.MkdirAll(plan.Temp, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", plan.Temp, err)
	}

	folder, err := os.MkdirTemp(plan.Temp, "komodo-base-")
	if err != nil {
		return nil, fmt.Errorf("make the plan folder: %w", err)
	}

	build := filepath.Join(folder, "build")
	if err := os.Mkdir(build, 0o755); err != nil {
		_ = os.RemoveAll(folder)
		return nil, fmt.Errorf("make the build cache folder: %w", err)
	}

	return &baseRunner{boxes: boxes, root: root, base: base, group: group, plan: plan, folder: folder, build: build}, nil
}

// Run checks out the base commit afresh, runs one shell command from its root and returns the exit code; a killed command is
// not 0. The checkout and its sandbox are removed afterwards.
func (b *baseRunner) Run(ctx context.Context, command string) (int, error) {
	// baseTimeout is the longest one command may run before the sandbox kills it.
	const baseTimeout = 10 * time.Minute

	b.runs++
	run := filepath.Join(b.folder, fmt.Sprintf("run-%d", b.runs))
	dir, tmp := filepath.Join(run, "base"), filepath.Join(run, "tmp")

	defer func() { _ = os.RemoveAll(run) }()

	if err := extractBase(ctx, b.root, b.base, dir); err != nil {
		return -1, fmt.Errorf("check out the base commit: %w", err)
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return -1, fmt.Errorf("make the sandbox folder: %w", err)
	}

	inst, err := b.boxes.CreateSandboxInstance(ctx, sandbox.Spec{
		Group:      b.group,
		Worktree:   dir,
		Writable:   []string{tmp, b.build},
		Modules:    b.plan.Modules,
		Toolchains: b.plan.Toolchains,
		MaxProcs:   b.plan.MaxProcs,
		MaxMemGB:   b.plan.MaxMemGB,
		Role:       sandbox.RoleBuilder,
	})
	if err != nil {
		return -1, fmt.Errorf("create the sandbox: %w", err)
	}

	defer func() { _ = b.boxes.DestroySandboxInstance(context.Background(), inst.ID) }()

	cmd, err := b.boxes.Command(ctx, inst.ID, sandbox.Command{Argv: []string{"sh", "-c", command}, Timeout: baseTimeout})
	if err != nil {
		return -1, err
	}

	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return -1, err
	}

	_ = sandbox.Wait(cmd)
	if cmd.ProcessState == nil {
		return -1, errors.New("the command did not finish")
	}

	return cmd.ProcessState.ExitCode(), nil
}

// Close deletes the plan's folder with every checkout and the build cache folder in it.
func (b *baseRunner) Close() error {
	return os.RemoveAll(b.folder)
}

// openBase opens the planner's base-commit runner: a fresh checkout of HEAD and sandbox per command.
func (k *repoKit) openBase(ctx context.Context) (intake.BaseRunner, error) {
	sum := sha256.Sum256([]byte(k.root))
	group := "plan-" + hex.EncodeToString(sum[:4])

	return openBaseRunner(ctx, k.s.boxes, k.root, group, k.s.sandboxPlan(group))
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
