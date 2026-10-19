package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// sessReq asks for one session of an agent.
type sessReq struct {
	agent  string
	task   string
	in     RoleInput
	resume *llm.ResumeHandle // a saved session to continue, when its link is chosen again
	input  string            // the one new message of a resumed session
}

// sessOut is how one session ended.
type sessOut struct {
	id      string
	reason  Reason
	outcome Outcome
	handle  llm.ResumeHandle
	limit   *llm.ProviderLimit
	result  llm.Result
}

// sessCtx is everything one running session owns.
type sessCtx struct {
	id      string
	dir     string
	sess    llm.Session
	watch   *toolWatch
	guard   *Guard
	memo    *judgeMemo
	closers []func()
	handle  llm.ResumeHandle
	resumed bool
}

// close releases what the session opened.
func (s *sessCtx) close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
}

// judgeMemo runs a role's Judge once per distinct result, so the tool loop's gate and the pipeline share one run.
type judgeMemo struct {
	role Role
	mu   sync.Mutex
	runs map[string]judged
}

// Check is the role's gate for the tool loop: a rejected result goes back into the session as text.
func (j *judgeMemo) Check(ctx context.Context, output json.RawMessage) (string, error) {
	_, err := j.judge(ctx, llm.Result{Output: output})
	var ge *GateError
	if errors.As(err, &ge) {
		return ge.Text, nil
	}
	return "", err
}

// judge returns the judgement of a result, running the role's Judge the first time only.
func (j *judgeMemo) judge(ctx context.Context, r llm.Result) (Outcome, error) {
	key := string(r.Output)

	j.mu.Lock()
	if v, ok := j.runs[key]; ok {
		j.mu.Unlock()
		return v.out, v.err
	}
	j.mu.Unlock()
	out, err := j.role.Judge(ctx, r)
	j.mu.Lock()
	if j.runs == nil {
		j.runs = map[string]judged{}
	}

	j.runs[key] = judged{out, err}
	j.mu.Unlock()
	return out, err
}

// judged is one stored judgement.
type judged struct {
	out Outcome
	err error
}

// guardFailure is a guard that could not be compiled; the session ends no_result and the task retries.
type guardFailure struct{ err error }

// Error describes the failure.
func (g *guardFailure) Error() string { return "compile the guard: " + g.err.Error() }

// gitReader adapts the Git to the diff and git_read tools of one lease.
type gitReader struct {
	git   Git
	lease LeaseRef
}

// Diff returns the group's diff against its base commit.
func (g gitReader) Diff(ctx context.Context, paths []string) (string, error) {
	return g.git.Diff(ctx, g.lease, paths)
}

// ReadOnly runs one read-only git command.
func (g gitReader) ReadOnly(ctx context.Context, command string, args []string) (string, error) {
	return g.git.ReadOnly(ctx, g.lease, command, args)
}

// runAgent runs one session of an agent to its end, walking the chain on a link that fails to start or a provider limit.
func (m *Manager) runAgent(ctx context.Context, req sessReq) (sessOut, error) {
	perm, hasPerm := m.spec.Agents[req.agent]

	role := m.c.Roles[req.agent]
	if !hasPerm || role == nil {
		return sessOut{}, block(
			"setup",
			map[string]string{"error": "the agent " + req.agent + " has no permissions or no role"},
		)
	}

	req.in.Group, req.in.Trip = m.spec.Group, m.Progress().Returns

	slots, err := role.Slots(ctx, req.in)
	if err != nil {
		return sessOut{}, fmt.Errorf("fill the %s slots: %w", req.agent, err)
	}

	brief, err := m.c.Briefs.Brief(ctx, req.agent, slots)
	if errors.Is(err, ErrContext) {
		return sessOut{}, block("context", map[string]string{"error": err.Error()})
	}
	if err != nil {
		return sessOut{}, err
	}

	var exclude []llm.Link

	for {
		if err := m.checkStop(ctx); err != nil {
			return sessOut{}, err
		}

		sel, err := m.c.Router.Select(
			ctx,
			SelectRequest{
				Chain:       agentLinks(perm.Chain),
				Weight:      llm.Weight(perm.Weight),
				BriefTokens: brief.Tokens,
				Exclude:     exclude,
			},
		)

		var pe *PausedError
		switch {
		case errors.As(err, &pe):
			return sessOut{}, m.pauseFor(ctx, pe.Limits, req.agent, req.resume, false)
		case errors.Is(err, ErrNoProvider):
			return sessOut{}, block("no_provider", nil)
		case errors.Is(err, ErrPaidRefused):
			return sessOut{}, block("paid_refused", map[string]string{"setting": "providers.paid"})
		case errors.Is(err, ErrContext):
			return sessOut{}, block("context", nil)
		case err != nil:
			return sessOut{}, err
		}

		for _, sk := range sel.Skips {
			m.fact(
				"provider.skip",
				map[string]string{
					"from":   string(sk.From),
					"to":     string(sk.To),
					"reason": sk.Reason,
				},
			)
		}
		if m.c.Limits != nil {
			ok, err := m.c.Limits.TakeSlot(sel.Link, sel.Profile.Slots)
			if err != nil {
				return sessOut{}, fmt.Errorf("take a slot on %s: %w", sel.Link, err)
			}
			if !ok {
				exclude = append(exclude, sel.Link)
				continue
			}
		}

		out, retry, err := m.sessionInSlot(ctx, req, role, perm, brief, sel)
		switch {
		case err != nil:
			return out, err
		case retry:
			exclude = append(exclude, sel.Link)
			req.resume = nil
		case out.reason == state.ReasonProvider:
			req.resume = nil
		default:
			return out, nil
		}
	}
}

// checkStop returns ErrStopped once a stop command or a cancelled context ended the work.
func (m *Manager) checkStop(ctx context.Context) error {
	if m.isStopped() || ctx.Err() != nil {
		return ErrStopped
	}
	return nil
}

// sessionInSlot runs one session and gives its link slot back when it ends, even by a panic.
func (m *Manager) sessionInSlot(
	ctx context.Context,
	req sessReq,
	role Role,
	perm Permissions,
	brief Brief,
	sel Selection,
) (sessOut, bool, error) {
	if m.c.Limits != nil {
		defer func() { _ = m.c.Limits.ReleaseSlot(sel.Link) }()
	}
	return m.oneSession(ctx, req, role, perm, brief, sel)
}

// oneSession starts, supervises and judges one session on a chosen link; retry is true when the link failed to start.
func (m *Manager) oneSession(
	ctx context.Context,
	req sessReq,
	role Role,
	perm Permissions,
	brief Brief,
	sel Selection,
) (out sessOut, retry bool, err error) {
	sc, err := m.prepare(req, role, perm, brief, sel)
	if err != nil {
		return sessOut{}, false, err
	}
	defer sc.close()

	sink, _ := role.(FindingSink)
	if err := m.bindTools(ctx, sc, req, role, perm, sink); err != nil {
		var gf *guardFailure
		if errors.As(err, &gf) {
			m.fact(
				"session.end",
				map[string]any{
					"session": sc.id,
					"agent":   req.agent,
					"reason":  state.ReasonNoResult,
					"error":   err.Error(),
				},
			)

			return sessOut{id: sc.id, reason: state.ReasonNoResult}, false, nil
		}

		return sessOut{}, false, err
	}

	defer func() {
		m.mu.Lock()
		delete(m.guards, sc.id)
		m.mu.Unlock()
	}()

	m.mu.Lock()
	if m.prog.Handles == nil {
		m.prog.Handles = map[string]llm.ResumeHandle{}
	}

	m.prog.Handles[req.agent] = sc.handle
	m.mu.Unlock()
	m.fact(
		"session.start",
		map[string]any{
			"session":    sc.id,
			"agent":      req.agent,
			"link":       sel.Link,
			"model":      sel.Model,
			"ctx":        sel.Profile.Ctx,
			"cost_class": sel.Profile.CostClass,
			"resume":     sc.resumed,
		},
	)

	if err := m.report(ctx); err != nil {
		return sessOut{}, false, err
	}

	stream, started, err := m.start(ctx, sc, req, sel)
	if err != nil {
		return m.startFailed(ctx, sc, req, err)
	}

	sc.resumed = started
	res, reason, serr := m.supervise(ctx, sc, req, stream)
	_ = stream.Close()

	out = sessOut{id: sc.id, reason: reason, handle: sc.handle}
	if res.Handle.Value != "" {
		out.handle = res.Handle
	}

	var pl *llm.ProviderLimit
	if errors.As(serr, &pl) {
		out.limit = pl
		if pl.Handle != nil {
			out.handle = *pl.Handle
		}
		_ = m.c.Router.RecordLimit(ctx, pl)
	}
	if reason == state.ReasonDone {
		out, err = m.judgeResult(ctx, sc, req, stream, sel, res, out)
	}

	m.endSession(ctx, sc, req, out)
	return out, false, err
}

// prepare makes the session folder and writes its files; a resumed session keeps its folder and brief.
func (m *Manager) prepare(req sessReq, role Role, perm Permissions, brief Brief, sel Selection) (*sessCtx, error) {
	id, resumed := "", false
	if req.resume != nil && req.resume.Link == sel.Link {
		switch req.resume.Kind {
		case "session_id":
			id, resumed = req.resume.Value, true
		case "messages_file":
			id, resumed = filepath.Base(filepath.Dir(req.resume.Value)), true
		}
	}
	if id == "" {
		var err error
		if id, err = newUUID(); err != nil {
			return nil, err
		}
	}

	dir, err := m.sessionDir(id)
	if err != nil {
		return nil, err
	}

	sc := &sessCtx{id: id, dir: dir, resumed: resumed, memo: &judgeMemo{role: role}}
	if !resumed {
		if err := writeBriefFiles(dir, brief.Parts); err != nil {
			return nil, err
		}
	}

	files := m.effectiveFiles()
	if err := writeJSON(dir, "scope.json", map[string]any{"files": files}); err != nil {
		return nil, err
	}

	rec := map[string]any{
		"session":      id,
		"group":        m.spec.Group,
		"task":         req.task,
		"agent":        req.agent,
		"link":         sel.Link,
		"model":        sel.Model,
		"cost_class":   sel.Profile.CostClass,
		"effort":       sel.Profile.Effort,
		"ctx":          sel.Profile.Ctx,
		"brief_tokens": brief.Tokens,
		"resume":       resumed,
	}
	if err := writeJSON(dir, "request.json", rec); err != nil {
		return nil, err
	}

	kind, val := "session_id", id
	if !programLink(sel.Link) {
		kind, val = "messages_file", filepath.Join(dir, "messages.jsonl")
	}

	sc.handle = llm.ResumeHandle{Link: sel.Link, Kind: kind, Value: val}

	sc.sess = llm.Session{
		ID:      id,
		Dir:     dir,
		Agent:   req.agent,
		Link:    sel.Link,
		Model:   sel.Model,
		Profile: sel.Profile,
		Cache:   m.c.Cache,
		Parts:   brief.Parts,
	}
	if r, ok := agent.RoleByName(req.agent); ok {
		sc.sess.Schema = r.Result
	}

	return sc, nil
}

// programLink reports whether a link runs a program that reads mcp.json and keeps its own session, so it resumes by id.
func programLink(l llm.Link) bool {
	return l == llm.ClaudeCLI || l == llm.OpenAISubscription
}

// sessionDir returns the folder of a session and makes it.
func (m *Manager) sessionDir(id string) (string, error) {
	dir := filepath.Join(m.sessionsRoot(), id)
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		return "", fmt.Errorf("make the session folder: %w", err)
	}
	return dir, nil
}

// effectiveFiles returns the group's file list including what scope answers added.
func (m *Manager) effectiveFiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.files)
}

// bindTools gives the session its tools: the bound registry on an API link, the tool server's entry on the subscription
// link.
func (m *Manager) bindTools(
	ctx context.Context,
	sc *sessCtx,
	req sessReq,
	role Role,
	perm Permissions,
	sink FindingSink,
) error {
	irole := instanceRole(req.agent)

	m.mu.Lock()
	inst := m.instances[irole]
	m.mu.Unlock()
	files := m.effectiveFiles()
	cfg := ToolServerConfig{
		Agent:        req.agent,
		Session:      sc.id,
		Worktree:     m.spec.Lease.Worktree,
		Dir:          sc.dir,
		Files:        files,
		Permissions:  perm,
		Commands:     m.c.Commands,
		Format:       m.c.Commands["format"],
		RefusalLimit: m.spec.Limits.RefusalLimit,
	}
	onFact := func(f Fact) { m.mu.Lock(); m.facts = append(m.facts, f); m.mu.Unlock() }
	onFind := func(s Submitted) {
		if sink != nil {
			sink.Submit(s)
		}
	}

	if programLink(sc.sess.Link) {
		exe := m.c.Exe
		if exe == "" {
			exe, _ = os.Executable()
		}

		raw, err := MCPConfig(exe, cfg)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(sc.dir, "mcp.json"), raw, 0o644); err != nil {
			return fmt.Errorf("write mcp.json: %w", err)
		}

		sc.watch = &toolWatch{
			dir:    sc.dir,
			limit:  m.spec.Limits.RefusalLimit,
			now:    m.now,
			onFact: onFact,
			onFind: onFind,
		}

		return nil
	}

	home, _ := os.UserHomeDir()
	rows, err := LoadRows(home)
	if err != nil {
		return err
	}

	bind := Binding{Agent: req.agent, Permissions: perm, Worktree: m.spec.Lease.Worktree}
	if req.agent == "builder" {
		bind.Files = files
	}

	g, err := NewGuard(rows, bind, m.spec.Limits.RefusalLimit)
	if err != nil {
		return &guardFailure{err}
	}

	env := &Env{
		Worktree:  m.spec.Lease.Worktree,
		Dir:       sc.dir,
		Confiner:  m.c.Sandboxes,
		Instance:  inst,
		Commands:  m.c.Commands,
		Format:    m.c.Commands["format"],
		Git:       gitReader{m.c.Git, m.spec.Lease},
		Skill:     m.c.Skill,
		OnFinding: onFind,
		OnFact:    onFact,
		Now:       m.now,
	}
	if req.agent == "builder" {
		env.Done = func(ctx context.Context, task string) (int, string, error) {
			return m.taskDone(ctx, sc, task)
		}
	}

	reg := NewRegistry(env)

	if len(perm.Servers) > 0 && m.c.Sandboxes != nil {
		getenv := m.c.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}

		conns, err := StartAllowed(ctx, m.c.Sandboxes, inst, perm, m.spec.Servers, getenv)
		if err != nil {
			return block("setup", map[string]string{"error": err.Error()})
		}

		for _, c := range conns {
			sc.closers = append(sc.closers, func() { _ = c.Close() })
			if err := reg.AddOutsideConn(ctx, c); err != nil {
				return block("setup", map[string]string{"error": err.Error()})
			}
			for _, w := range c.Warnings() {
				m.fact("sandbox.warning", map[string]string{"warning": w})
			}
		}
	}

	b, err := reg.Bind(perm.Tools, g)
	if err != nil {
		return block("setup", map[string]string{"error": err.Error()})
	}

	sc.guard = g
	sc.watch = &toolWatch{inner: b, limit: m.spec.Limits.RefusalLimit, now: m.now}
	sc.sess.Tools = sc.watch
	sc.sess.Gate = sc.memo

	m.mu.Lock()
	m.guards[sc.id] = g
	m.mu.Unlock()
	return nil
}

// start begins or resumes the session's stream; a resume that fails other than by a provider limit falls back to a fresh
// start.
func (m *Manager) start(ctx context.Context, sc *sessCtx, req sessReq, sel Selection) (llm.Stream, bool, error) {
	if sc.resumed {
		st, err := m.c.Client.Resume(ctx, sc.sess, llm.Input{Text: cmpOr(req.input, "Continue where you stopped.")})

		var pl *llm.ProviderLimit
		switch {
		case err == nil:
			return st, true, nil
		case errors.As(err, &pl):
			return nil, true, err
		}

		m.fact("resume_lost", map[string]string{"session": sc.id, "error": err.Error()})

		id, uerr := newUUID()
		if uerr != nil {
			return nil, false, uerr
		}

		sc.id, sc.sess.ID = id, id

		kind, val := "session_id", id
		if !programLink(sel.Link) {
			kind, val = "messages_file", filepath.Join(sc.dir, "messages.jsonl")
			_ = os.Remove(val)
		}

		sc.handle = llm.ResumeHandle{Link: sel.Link, Kind: kind, Value: val}

		sc.resumed = false
		if req.input != "" {
			sc.sess.Parts[4] = strings.TrimSpace(sc.sess.Parts[4] + "\n\n" + req.input)
		}
		if err := writeBriefFiles(sc.dir, sc.sess.Parts); err != nil {
			return nil, false, err
		}
	}

	st, err := m.c.Client.Start(ctx, sc.sess)
	if errors.Is(err, llm.ErrModelGone) && m.c.Router.Catalog != nil {
		if prior, ok := m.c.Router.Catalog.Prior(sc.sess.Model); ok {
			sc.sess.Model = prior.ID
			st, err = m.c.Client.Start(ctx, sc.sess)
		}
	}

	return st, false, err
}

// startFailed maps a failed start to the session's result: a billing refusal, a provider limit, a link to skip or a crash.
func (m *Manager) startFailed(ctx context.Context, sc *sessCtx, req sessReq, err error) (sessOut, bool, error) {
	out := sessOut{id: sc.id, handle: sc.handle}

	m.dropHandle(req.agent)

	var pl *llm.ProviderLimit
	switch {
	case errors.As(err, &pl):
		_ = m.c.Router.RecordLimit(ctx, pl)
		if sc.resumed {
			return out, false, m.pauseFor(ctx, []*llm.ProviderLimit{pl}, req.agent, req.resume, true)
		}
		out.reason, out.limit = state.ReasonProvider, pl
		return out, true, nil
	case errors.Is(err, llm.ErrBilling):
		out.reason = state.ReasonBilling
		m.endSession(ctx, sc, req, out)
		return out, false, nil
	case errors.Is(err, llm.ErrUnavailable),
		errors.Is(err, llm.ErrUnauthorized),
		errors.Is(err, llm.ErrNotYet),
		errors.Is(err, llm.ErrUnpriced):
		m.fact("provider.skip", map[string]string{"from": string(sc.sess.Link), "reason": unusable(err)})
		return out, true, nil
	case ctx.Err() != nil:
		return out, false, ErrStopped
	}

	out.reason = state.ReasonCrash
	m.fact(
		"session.end",
		map[string]any{
			"session": sc.id,
			"agent":   req.agent,
			"reason":  out.reason,
			"error":   err.Error(),
		},
	)

	return out, false, nil
}

// dropHandle forgets an agent's saved session handle.
func (m *Manager) dropHandle(agentName string) {
	m.mu.Lock()
	delete(m.prog.Handles, agentName)
	m.mu.Unlock()
}

// endSession records the end of a session: its handle, its refusals, result.json and the session.end fact.
func (m *Manager) endSession(ctx context.Context, sc *sessCtx, req sessReq, out sessOut) {
	if out.reason != state.ReasonProvider && out.reason != state.ReasonPaused {
		m.dropHandle(req.agent)
	}
	if req.agent == "builder" {
		hh := out.handle
		m.mu.Lock()
		m.builderH = &hh
		m.mu.Unlock()
	}

	refusals := sc.watch.Refusals()

	m.mu.Lock()
	m.prog.Refusals = refusals
	m.mu.Unlock()

	data := map[string]any{
		"session": sc.id,
		"agent":   req.agent,
		"reason":  out.reason,
		"link":    sc.sess.Link,
		"model":   sc.sess.Model,
	}
	if out.reason == state.ReasonDone && out.outcome.Kind == OutcomeScope {
		data["scope_paths"] = out.outcome.Paths
		data["scope_reason"] = out.outcome.Reason

		// A request the Task Manager denies itself ends the session done: nobody is asked, so the group is not blocked.
		if _, why, err := m.scopeVerdict(req.task, out.outcome.Paths); err != nil || why == "" {
			data["reason"] = state.ReasonNeedsScope
		}
	}
	if u := out.result.Usage; u != nil {
		data["tokens"] = int64(u.TokensIn + u.TokensOut)
		data["usage"] = bus.SessionUsage{
			TokensIn:   u.TokensIn,
			TokensOut:  u.TokensOut,
			CacheRead:  u.CacheRead,
			CacheWrite: u.CacheWrite,
			USD:        u.USD,
			USDEquiv:   u.USDEquiv,
		}
	}
	if req.agent == "builder" && out.reason != state.ReasonDone && m.c.Git != nil {
		st, err := m.c.Git.ReadOnly(ctx, m.spec.Lease, "status", []string{"--porcelain"})
		if files := changedPaths(st); err == nil && len(files) > 0 {
			data["unfinished"] = map[string]any{"task": req.task, "files": files}

			m.mu.Lock()
			m.prog.Unfinished = &state.UnfinishedEdits{Task: req.task, Files: files}
			m.mu.Unlock()
		}
	}

	m.fact("session.end", data)

	if u := out.result.Usage; u != nil {
		m.fact(
			"usage",
			map[string]any{
				"link":        sc.sess.Link,
				"agent":       req.agent,
				"session":     sc.id,
				"model":       sc.sess.Model,
				"tokens_in":   u.TokensIn,
				"tokens_out":  u.TokensOut,
				"cache_read":  u.CacheRead,
				"cache_write": u.CacheWrite,
				"usd":         u.USD,
				"usd_equiv":   u.USDEquiv,
			},
		)
	}

	_ = writeJSON(
		sc.dir,
		"result.json",
		map[string]any{
			"session":     sc.id,
			"exit_reason": out.reason,
			"result":      json.RawMessage(orNull(out.result.Output)),
			"usage":       out.result.Usage,
			"handle":      out.handle,
		},
	)
}

// supervise reads the stream to its end under the session clocks and the Runtime Manager's timer answers.
func (m *Manager) supervise(ctx context.Context, sc *sessCtx, req sessReq, st llm.Stream) (llm.Result, Reason, error) {
	now := m.now()
	clk := &Clocks{Started: now, LastLine: now}
	lim := SessionLimits{
		Wall: cmpOrDur(m.spec.Limits.SessionWall, time.Hour),
		Idle: cmpOrDur(m.spec.Limits.SessionIdle, 10*time.Minute),
	}
	inject := make(chan string, 4)
	ls := &liveSession{agent: req.agent, inject: inject}

	m.mu.Lock()
	m.live[sc.id] = ls

	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.live, sc.id)
		m.mu.Unlock()
	}()

	raw, err := os.OpenFile(filepath.Join(sc.dir, "stream.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		defer func() { _ = raw.Close() }()
	}

	h := Hooks{
		Now:       m.now,
		Every:     m.c.Tick,
		TermAfter: m.c.Ladder[0],
		KillAfter: m.c.Ladder[1],
		Inject:    inject,
		Extend: func(ctx context.Context, used, limit time.Duration) (time.Duration, error) {
			g, err := m.rt.ExtendTimer(ctx, TimerRequest{Session: sc.id, Used: used, Limit: limit})
			return g.Extra, err
		},
		Fact: func(sub string, d map[string]any) {
			d["session"] = sc.id
			m.fact(sub, d)
		},
		Raw: func(line []byte) {
			if raw != nil {
				_, _ = raw.Write(append(append([]byte{}, line...), '\n'))
			}
			sc.watch.poll()
		},
		Tools: sc.watch.Tools, Halt: sc.watch.Halt,
	}

	return SuperviseWith(ctx, st, lim, clk, h)
}

// judgeResult runs the role's Judge on a finished result; on the subscription link one rejected result is sent back as a
// resume.
func (m *Manager) judgeResult(
	ctx context.Context,
	sc *sessCtx,
	req sessReq,
	st llm.Stream,
	sel Selection,
	res llm.Result,
	out sessOut,
) (sessOut, error) {
	out.result = res
	oc, err := sc.memo.judge(ctx, res)

	var ge *GateError
	if errors.As(err, &ge) && programLink(sel.Link) {
		retry, rerr := m.c.Client.Resume(ctx, sc.sess, llm.Input{Text: ge.Text})
		if rerr == nil {
			res2, reason, serr := m.supervise(ctx, sc, req, retry)
			_ = retry.Close()
			if reason != state.ReasonDone {
				out.reason = reason
				return out, nil
			}
			_ = serr
			res2.Usage = addUsage(res.Usage, res2.Usage)
			res, out.result = res2, res2
			oc, err = sc.memo.judge(ctx, res)
		}
	}

	if errors.As(err, &ge) {
		out.reason = state.ReasonCheckFail
		if ge.Schema {
			out.reason = state.ReasonSchemaFail
		}
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("judge the %s result: %w", req.agent, err)
	}

	out.outcome = oc
	if res.Handle.Value != "" {
		out.handle = res.Handle
	}

	return out, nil
}

// addUsage returns the sum of two usage counts; either may be nil.
func addUsage(a, b *llm.Usage) *llm.Usage {
	if a == nil || b == nil {
		if a == nil {
			return b
		}
		return a
	}

	return &llm.Usage{
		TokensIn:   a.TokensIn + b.TokensIn,
		TokensOut:  a.TokensOut + b.TokensOut,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
		USD:        a.USD + b.USD,
		USDEquiv:   a.USDEquiv + b.USDEquiv,
	}
}

// newUUID returns a random version 4 identifier.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a session identifier: %w", err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// agentLinks converts an agent's chain to links.
func agentLinks(chain []string) []llm.Link {
	out := make([]llm.Link, len(chain))
	for i, c := range chain {
		out[i] = llm.Link(c)
	}
	return out
}

// writeJSON writes a file of the session folder as indented JSON.
func writeJSON(dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// writeBriefFiles writes brief.md and system.md into a session folder.
func writeBriefFiles(dir string, parts [5]string) error {
	sys, rerr := komodo.Embedded.ReadFile("embed/output/standard.md")
	if rerr != nil {
		sys = []byte("Reply with the final result in the requested shape.\n")
	}

	files := map[string]string{"brief.md": briefText(Brief{Parts: parts}), "system.md": string(sys)}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return nil
}

// briefText joins the non-empty brief parts with a blank line.
func briefText(b Brief) string {
	var parts []string
	for _, p := range b.Parts {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n")
}

// instanceRole returns the sandbox role whose instance an agent's commands run in.
func instanceRole(agentName string) string {
	if agentName == "builder" {
		return "builder"
	}
	return "tester"
}

// changedPaths lists the paths of a porcelain status, dropping the two status letters; a rename gives its new path.
func changedPaths(status string) []string {
	var out []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if _, to, ok := strings.Cut(p, " -> "); ok {
			p = to
		}
		out = append(out, strings.Trim(p, `"`))
	}
	return out
}

// orNull returns raw, or JSON null when it is empty.
func orNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}
