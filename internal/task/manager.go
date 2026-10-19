package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// blockErr is a pipeline step that cannot go on: the Manager escalates it and holds the group.
type blockErr struct {
	kind   EscalationKind
	reason string
	detail any
}

// Error describes the escalation.
func (e *blockErr) Error() string { return string(e.kind) + " " + e.reason }

// failedErr is the end of a group that cannot continue.
type failedErr struct{ reason string }

// Error describes the failure.
func (e *failedErr) Error() string { return "the group failed: " + e.reason }

// liveSession is one running session as the Manager tracks it.
type liveSession struct {
	agent  string
	inject chan string
}

// pauseCmd is a pause the Runtime Manager asked for.
type pauseCmd struct {
	link  string
	until time.Time
}

// Manager runs one task group's pipeline; the Runtime Manager starts one per group.
type Manager struct {
	spec GroupSpec
	c    Collaborators
	rt   Runtime

	runMu sync.Mutex // one Run, Resume or Continue at a time

	mu         sync.Mutex
	prog       Progress
	facts      []Fact
	files      []string
	open       *Escalation
	pauseReq   *pauseCmd
	lastPause  PauseState
	running    bool
	stopped    bool
	stopCause  string
	begun      bool
	live       map[string]*liveSession
	guards     map[string]*Guard
	instances  map[string]string
	insts      map[string]sandbox.Instance
	verdicts   map[string]Outcome
	pending    []string
	regression bool // the last failed checks found a test that passed at the base
	scopeWait  *scopeWait
	builderH   *llm.ResumeHandle
	gateFails  map[string]int
	contract   int
	ids        atomic.Int64
	tag        string // makes the identifiers differ from those of an earlier Manager of the same group
	stopCtx    context.Context
	stopCancel context.CancelCauseFunc
	closed     chan struct{}
	closeOnce  sync.Once
	closeErr   error
	tmp        string
	scopeInput string     // the scope answer text the builder's next session receives
	taskMu     sync.Mutex // one completion command at a time
	closing    atomic.Bool
}

// New builds a Manager from the group's spec and the collaborators it calls.
func New(spec GroupSpec, c Collaborators) (*Manager, error) {
	// Defaults the Task Manager applies to a limit the Runtime Manager left at zero.
	const defaultRefusals = 3
	if spec.Lease.Worktree == "" {
		return nil, errors.New("the group spec has no worktree")
	}
	if spec.Limits.RefusalLimit == 0 {
		spec.Limits.RefusalLimit = defaultRefusals
	}

	m := &Manager{
		spec:      spec,
		c:         c,
		rt:        c.Runtime,
		live:      map[string]*liveSession{},
		guards:    map[string]*Guard{},
		instances: map[string]string{},
		insts:     map[string]sandbox.Instance{},
		verdicts:  map[string]Outcome{},
		gateFails: map[string]int{},
		closed:    make(chan struct{}),
		files:     slices.Clone(spec.Files),
		tag:       newTag(),
	}

	m.stopCtx, m.stopCancel = context.WithCancelCause(context.Background())
	m.prog.RetriesLeft = spec.Limits.GroupRetries
	if sv := spec.Saved; sv != nil {
		m.prog = cloneProgress(*sv)
		for _, w := range sv.Widened {
			if !slices.Contains(m.files, w) {
				m.files = append(m.files, w)
			}
		}
	}

	switch m.prog.Stage {
	case state.StageBuild, state.StageTestReview, state.StageCommit, state.StageCleanup:
	default:
		m.prog.Stage = state.StageBuild
	}
	if m.prog.Refusals == nil {
		m.prog.Refusals = map[string]int{}
	}

	return m, nil
}

// Progress returns a copy of the group's counters.
func (m *Manager) Progress() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneProgress(m.prog)
}

// Run runs the pipeline from the saved stage, or from build for a new group, and returns when the group is closed, held or
// stopped.
func (m *Manager) Run(ctx context.Context) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	ctx, done := m.workCtx(ctx)
	defer done()
	return m.finish(ctx, m.drive(ctx))
}

// Resume continues a paused group at its timer; it starts owed agents first.
func (m *Manager) Resume(ctx context.Context, w Wake) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	m.mu.Lock()

	p := m.prog.Pause
	if p == nil {
		m.mu.Unlock()
		return ErrNotPaused
	}

	m.lastPause = *p
	m.lastPause.Owed = slices.Clone(p.Owed)
	m.prog.Pause = nil
	m.mu.Unlock()
	m.fact("group.resume", map[string]any{"reason": w.Reason, "owed": m.lastPause.Owed})

	ctx, done := m.workCtx(ctx)
	defer done()

	return m.finish(ctx, m.drive(ctx))
}

// Continue delivers the one Answer to an Escalation and continues the held group.
func (m *Manager) Continue(ctx context.Context, a Answer) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	m.mu.Lock()

	esc := m.open
	if esc == nil || esc.ID != a.Escalation {
		m.mu.Unlock()
		return ErrNoEscalation
	}

	m.open = nil
	m.mu.Unlock()

	ctx, done := m.workCtx(ctx)
	defer done()

	if esc.Kind == EscScope {
		m.fact("scope.answer", map[string]any{"action": a.Action, "approved": a.Approved})
		if err := m.applyScopeAnswer(ctx, a); err != nil {
			return m.finish(ctx, err)
		}
	} else if a.Action == "deny" {
		return m.finish(ctx, &failedErr{reason: "the answer denied the retry"})
	}

	return m.finish(ctx, m.drive(ctx))
}

// Pause holds the group after its current step; Resume releases it at the reset time.
func (m *Manager) Pause(ctx context.Context, link string, until time.Time) error {
	m.mu.Lock()
	if m.prog.Pause != nil || m.pauseReq != nil {
		m.mu.Unlock()
		return nil
	}

	m.pauseReq = &pauseCmd{link: link, until: until}
	running := m.running
	m.mu.Unlock()
	if running {
		return nil
	}
	if err := m.checkpoint(ctx); err != nil && !errors.Is(err, ErrGroupPaused) {
		return err
	}

	return nil
}

// Stop cancels live sessions with a cause and returns.
func (m *Manager) Stop(ctx context.Context, cause string) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}

	m.stopped, m.stopCause = true, cause
	running := m.running
	m.mu.Unlock()
	m.stopCancel(ErrStopped)
	m.fact("group.stop", map[string]string{"cause": cause})
	if cause == string(state.ReasonPauseTimeout) && !running {
		if err := m.escalate(ctx, &blockErr{kind: EscBlocked, reason: cause}); err != nil && !errors.Is(err, ErrHeld) {
			return err
		}
	}

	return nil
}

// Nudge injects the token-cap line into every live session.
func (m *Manager) Nudge() {
	const capNudgeJudge = "The group is at 90% of its token cap; finish and report your result now"

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ls := range m.live {
		line := capNudgeJudge
		if ls.agent == "builder" {
			line = capNudgeBuilder
		}
		select {
		case ls.inject <- line:
		default:
		}
	}
}

// CloseTaskGroup ends the group: it destroys the sandbox instances and reports the closing state.
func (m *Manager) CloseTaskGroup(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.closing.Store(true)
		m.mu.Lock()
		liveNow := len(m.live) > 0
		m.mu.Unlock()
		if liveNow {
			m.stopCancel(ErrStopped)
		}

		var errs []error

		m.mu.Lock()

		ids := make([]string, 0, len(m.instances))
		for _, id := range m.instances {
			ids = append(ids, id)
		}

		m.instances = map[string]string{}
		m.prog.Stage = state.StageDone
		m.mu.Unlock()
		if m.c.Sandboxes != nil {
			for _, id := range ids {
				if err := m.c.Sandboxes.DestroySandboxInstance(ctx, id); err != nil {
					errs = append(errs, err)
				}
			}
		}

		m.fact("group.closed", nil)
		if m.rt != nil {
			if err := m.report(ctx); err != nil {
				errs = append(errs, err)
			}
		}

		m.closeErr = errors.Join(errs...)
		close(m.closed)
	})

	return m.closeErr
}

// Runner returns the command runner bound to the sandbox instance of a role, for the role adapters built before the Manager.
func (m *Manager) Runner(role string) agent.CommandRunner { return m.runner(role) }

// Files returns the group's file list including what scope answers added.
func (m *Manager) Files() []string { return m.effectiveFiles() }

// setStage moves the group to a stage and reports it.
func (m *Manager) setStage(ctx context.Context, st state.Stage) error {
	m.mu.Lock()
	prev := m.prog.Stage
	m.prog.Stage = st
	findings := slices.Clone(m.pending)
	tree := m.prog.TreeAtReturn
	regression := m.regression
	m.mu.Unlock()

	switch {
	case prev == st:
	case prev == state.StageTestReview && st == state.StageBuild:
		m.fact(
			"result.fail",
			map[string]any{
				"items":      findings,
				"tree":       tree,
				"regression": regression,
				"findings":   strings.Join(findings, "\n"),
			},
		)
	default:
		// The Cleanup Handler closes the group inside the publish, which sets the stage to done before cleanup is entered.
		if prev == state.StageDone && st == state.StageCleanup {
			prev = state.StageCommit
		}
		m.fact("stage.done", map[string]any{"stage": prev})
	}

	return m.report(ctx)
}

// fact queues one fact for the next state report.
func (m *Manager) fact(subtype string, data any) {
	f := Fact{Subtype: subtype, At: m.now()}
	if data != nil {
		if raw, err := json.Marshal(data); err == nil {
			f.Data = raw
		}
	}
	m.mu.Lock()
	m.facts = append(m.facts, f)
	m.mu.Unlock()
}

// now returns the current time.
func (m *Manager) now() time.Time {
	if m.c.Now != nil {
		return m.c.Now()
	}
	return time.Now()
}

// report hands the counters and the queued facts to the Runtime Manager.
func (m *Manager) report(ctx context.Context) error {
	m.mu.Lock()
	s := StateReport{Group: m.spec.Group, Progress: cloneProgress(m.prog), Facts: m.facts}
	m.facts = nil
	m.mu.Unlock()
	if m.rt == nil {
		return errors.New("the manager has no runtime to report to")
	}
	return m.rt.ReportState(ctx, s)
}

// workCtx returns a context that ends when the caller's does or when a stop command arrives.
func (m *Manager) workCtx(ctx context.Context) (context.Context, func()) {
	c, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(m.stopCtx, func() { cancel(ErrStopped) })
	return c, func() { stop(); cancel(nil) }
}

// drive starts the group once and runs the stages until one ends the call.
func (m *Manager) drive(ctx context.Context) error {
	m.mu.Lock()
	m.running = true

	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	if err := m.begin(ctx); err != nil {
		return err
	}

	for {
		if m.isStopped() || ctx.Err() != nil {
			return ErrStopped
		}
		if err := m.checkpoint(ctx); err != nil {
			return err
		}

		var err error

		switch m.Progress().Stage {
		case state.StageBuild:
			err = m.runBuild(ctx)
		case state.StageTestReview:
			err = m.runTestReview(ctx)
		case state.StageCommit:
			err = m.runCommit(ctx)
		case state.StageCleanup:
			return m.awaitClose(ctx)
		default:
			return nil
		}

		if err != nil {
			return err
		}
	}
}

// begin creates the worktree for a new group, records the .git pointer and reports the first state.
func (m *Manager) begin(ctx context.Context) error {
	m.mu.Lock()
	done := m.begun
	m.begun = true
	m.mu.Unlock()
	if done {
		return nil
	}
	if m.rt == nil {
		return errors.New("the manager has no runtime")
	}
	if _, err := os.Stat(m.spec.Lease.Worktree); err != nil {
		if m.c.Git == nil {
			return errors.New("the manager has no git to create the worktree")
		}
		if err := m.c.Git.AddWorktree(ctx, m.spec.Lease); err != nil {
			return block("setup", map[string]string{"error": err.Error()})
		}
	}
	if pr, ok := m.c.Git.(PointerRecorder); ok {
		if err := pr.RecordPointer(m.spec.Lease); err != nil {
			return block("contract", map[string]string{"error": err.Error()})
		}
	}
	if m.spec.Saved == nil {
		m.fact("ready", nil)
	}
	// A group continued from a commit stage runs test and review again before the commit.
	if m.Progress().Stage == state.StageCommit {
		m.mu.Lock()
		m.prog.Stage = state.StageTestReview
		m.mu.Unlock()
	}

	return m.report(ctx)
}

// isStopped reports whether a stop command arrived.
func (m *Manager) isStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

// checkpoint holds the group when a pause command arrived.
func (m *Manager) checkpoint(ctx context.Context) error {
	m.mu.Lock()

	req := m.pauseReq
	if m.prog.Pause != nil {
		m.mu.Unlock()
		return ErrGroupPaused
	}
	if req == nil {
		m.mu.Unlock()
		return nil
	}

	m.pauseReq = nil

	until := req.until
	if until.IsZero() {
		until = m.now().Add(unknownReset)
	}

	since := m.now()
	if !m.lastPause.Since.IsZero() {
		since = m.lastPause.Since
	}

	m.prog.Pause = &PauseState{Since: since, WaitUntil: until, WaitLink: req.link}
	m.mu.Unlock()
	if err := m.report(ctx); err != nil {
		return err
	}

	return ErrGroupPaused
}

// awaitClose waits in the cleanup stage until the Cleanup Handler closes the group.
func (m *Manager) awaitClose(ctx context.Context) error {
	select {
	case <-m.closed:
		return nil
	case <-ctx.Done():
		return ErrStopped
	}
}

// finish turns the end of a call into its return value: an escalation holds the group, a stop returns nil.
func (m *Manager) finish(ctx context.Context, err error) error {
	var be *blockErr
	switch {
	case err == nil:
		return nil
	case errors.As(err, &be):
		return m.escalate(context.WithoutCancel(ctx), be)
	case errors.Is(err, ErrStopped) || errors.Is(context.Cause(ctx), ErrStopped):
		return nil
	}

	return err
}

// escalate hands an escalation to the Runtime Manager and holds the group.
func (m *Manager) escalate(ctx context.Context, e *blockErr) error {
	var raw json.RawMessage
	if e.detail != nil {
		raw, _ = json.Marshal(e.detail)
	}

	esc := Escalation{
		ID:     m.nextID("esc"),
		Group:  m.spec.Group,
		Kind:   e.kind,
		Reason: e.reason,
		Detail: raw,
	}
	m.mu.Lock()
	m.open = &esc
	m.mu.Unlock()
	if err := m.report(ctx); err != nil {
		return err
	}
	if err := m.rt.Escalate(ctx, esc); err != nil {
		return fmt.Errorf("escalate %s: %w", e.reason, err)
	}
	if e.kind == EscFailed {
		return &failedErr{reason: e.reason}
	}

	return ErrHeld
}

// nextID returns a unique identifier with a prefix.
func (m *Manager) nextID(prefix string) string {
	return prefix + "-" + m.tag + "-" + strconv.FormatInt(m.ids.Add(1), 10)
}

// instance returns the sandbox instance of a role, creating it and running the setup command on first use.
func (m *Manager) instance(ctx context.Context, role string) (string, error) {
	m.mu.Lock()
	if id := m.instances[role]; id != "" {
		m.mu.Unlock()
		return id, nil
	}
	m.mu.Unlock()
	if m.c.Sandboxes == nil {
		return "", block("setup", map[string]string{"error": "no sandbox manager is set"})
	}
	if m.tmp == "" {
		dir, err := os.MkdirTemp(m.c.Plan.Temp, "komodo-sandbox-")
		if err != nil {
			return "", fmt.Errorf("make the sandbox folder: %w", err)
		}
		m.tmp = dir
	}

	tmp := filepath.Join(m.tmp, role)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", fmt.Errorf("make the sandbox temporary folder: %w", err)
	}

	build := m.c.Plan.Build
	if build == "" {
		build = filepath.Join(m.tmp, role+"-build")
		if err := os.MkdirAll(build, 0o755); err != nil {
			return "", fmt.Errorf("make the build cache folder: %w", err)
		}
	}

	setup := m.c.Commands["setup"]
	spec := sandbox.Spec{
		Group:      m.spec.Group,
		Worktree:   m.spec.Lease.Worktree,
		GitDir:     m.spec.Lease.GitDir,
		Writable:   []string{tmp, build},
		Modules:    m.c.Plan.Modules,
		Toolchains: m.c.Plan.Toolchains,
		MaxProcs:   m.c.Plan.MaxProcs,
		MaxMemGB:   m.c.Plan.MaxMemGB,
		Role:       role,
		Network:    len(setup) > 0,
	}

	inst, err := m.c.Sandboxes.CreateSandboxInstance(ctx, spec)
	if err != nil {
		return "", block("setup", map[string]string{"error": err.Error(), "role": role})
	}

	m.mu.Lock()
	m.instances[role], m.insts[role] = inst.ID, inst
	m.mu.Unlock()

	if inst.Fallback {
		m.fact(
			"sandbox.fallback",
			map[string]string{
				"role":   role,
				"wanted": "docker",
				"used":   inst.Backend,
				"why":    inst.Reason,
			},
		)
	}

	if len(setup) > 0 {
		out, exit, err := m.runIn(ctx, inst.ID, setup, filepath.Join(m.tmp, role+"-setup"))
		if err != nil || exit != 0 {
			detail := map[string]string{"role": role, "output": out}
			if err != nil {
				detail["error"] = err.Error()
			}
			return "", block("setup", detail)
		}
		if err := m.c.Sandboxes.EndSetup(ctx, inst.ID); err != nil {
			return "", block("setup", map[string]string{"error": err.Error(), "role": role})
		}
	}

	return inst.ID, nil
}

// runIn runs one command in an instance and returns its capped output and exit code.
func (m *Manager) runIn(ctx context.Context, id string, argv []string, logDir string) (string, int, error) {
	if g, err := m.scopeGuard(); err == nil {
		if err := m.c.Sandboxes.SetMasked(id, g.SecretFiles()); err != nil {
			return "", -1, err
		}
	}

	cmd, err := m.c.Sandboxes.Command(ctx, id, sandbox.Command{Argv: argv, Timeout: runTimeout})
	if err != nil {
		return "", -1, err
	}

	res := runConfined(ctx, cmd, filepath.Join(logDir, "run-"+strconv.FormatInt(m.ids.Add(1), 10)+".log"))
	if res.StartErr != nil {
		return "", -1, res.StartErr
	}

	return res.Reply.Text, res.Exit, nil
}

// runner returns the command runner bound to the instance of a role; a class name stands for its command.
func (m *Manager) runner(role string) agent.CommandRunner {
	return func(ctx context.Context, command string) (int, string, error) {
		m.mu.Lock()
		id := m.instances[role]
		m.mu.Unlock()
		if id == "" {
			return -1, "", fmt.Errorf("the %s has no sandbox instance", role)
		}

		argv, ok := m.c.Commands[command]
		if !ok {
			argv = []string{"sh", "-c", command}
		}
		if len(argv) == 0 {
			return -1, "", errors.New("the command is empty")
		}

		out, exit, err := m.runIn(ctx, id, argv, filepath.Join(m.sessionsRoot(), "runs"))
		return exit, out, err
	}
}

// sessionsRoot returns the folder that holds the session folders, making a temporary one when none is set.
func (m *Manager) sessionsRoot() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.c.SessionsDir == "" {
		dir, err := os.MkdirTemp("", "komodo-sessions-")
		if err != nil {
			return os.TempDir()
		}
		m.c.SessionsDir = dir
	}

	return m.c.SessionsDir
}

// scopeWait is a builder's open scope request.
type scopeWait struct {
	task  string
	paths []string
}

// ErrStopped is returned inside the pipeline when a stop command ended the work; Run turns it into a nil return.
var ErrStopped = errors.New("the group was stopped")

// cloneProgress copies a Progress so a report never shares its maps and slices with the live one.
func cloneProgress(p Progress) Progress {
	p.TasksDone = slices.Clone(p.TasksDone)
	p.ScopeAsked = slices.Clone(p.ScopeAsked)

	p.Widened = slices.Clone(p.Widened)
	if p.Unfinished != nil {
		u := *p.Unfinished
		u.Files = slices.Clone(u.Files)
		p.Unfinished = &u
	}
	if p.Refusals != nil {
		r := make(map[string]int, len(p.Refusals))
		for k, v := range p.Refusals {
			r[k] = v
		}
		p.Refusals = r
	}
	if p.Handles != nil {
		h := make(map[string]llm.ResumeHandle, len(p.Handles))
		for k, v := range p.Handles {
			h[k] = v
		}
		p.Handles = h
	}
	if p.Pause != nil {
		q := *p.Pause
		q.Owed = slices.Clone(q.Owed)
		if q.Handle != nil {
			hh := *q.Handle
			q.Handle = &hh
		}
		p.Pause = &q
	}

	return p
}

// block builds the error that holds the group with an escalation.
func block(reason string, detail any) error {
	return &blockErr{kind: EscBlocked, reason: reason, detail: detail}
}
