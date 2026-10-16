package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// hostBackend is a sandbox mechanism that starts every command on the host in the instance's worktree.
type hostBackend struct {
	mu       sync.Mutex
	n        int
	prepared []sandbox.Spec
	released []string
}

func (b *hostBackend) Name() string { return "host" }
func (b *hostBackend) Check() error { return nil }

func (b *hostBackend) Prepare(_ context.Context, s sandbox.Spec) (sandbox.Instance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	b.prepared = append(b.prepared, s)
	return sandbox.Instance{ID: fmt.Sprintf("inst-%d", b.n), Group: s.Group, Backend: "host"}, nil
}

func (b *hostBackend) Command(ctx context.Context, _ sandbox.Instance, s sandbox.Spec, c sandbox.Command) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = s.Worktree
	killGroupOnCancel(cmd)
	return cmd, nil
}

func (b *hostBackend) Release(_ context.Context, i sandbox.Instance) error {
	b.mu.Lock()
	b.released = append(b.released, i.ID)
	b.mu.Unlock()
	return nil
}

// fakeRT is the Runtime Manager: it keeps every report and escalation and grants no extra time.
type fakeRT struct {
	mu     sync.Mutex
	stages []state.Stage
	last   Progress
	facts  []Fact
	escs   []Escalation
}

func (r *fakeRT) Escalate(_ context.Context, e Escalation) error {
	r.mu.Lock()
	r.escs = append(r.escs, e)
	r.mu.Unlock()
	return nil
}

func (r *fakeRT) ExtendTimer(context.Context, TimerRequest) (TimerGrant, error) {
	return TimerGrant{}, nil
}

func (r *fakeRT) ReportState(_ context.Context, s StateReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.stages); n == 0 || r.stages[n-1] != s.Progress.Stage {
		r.stages = append(r.stages, s.Progress.Stage)
	}
	r.last = s.Progress
	r.facts = append(r.facts, s.Facts...)
	return nil
}

// stagesSeen returns the stages in the order they were first reported.
func (r *fakeRT) stagesSeen() []state.Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.stages)
}

// factCount counts the reported facts of a subtype.
func (r *fakeRT) factCount(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range r.facts {
		if f.Subtype == sub {
			n++
		}
	}
	return n
}

// escalations returns the escalations raised so far.
func (r *fakeRT) escalations() []Escalation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.escs)
}

// progress returns the last reported counters.
func (r *fakeRT) progress() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneProgress(r.last)
}

// fakeBriefs assembles a fixed-size brief and keeps the slots each agent received.
type fakeBriefs struct {
	mu    sync.Mutex
	slots map[string][]map[string]string
}

func (f *fakeBriefs) Brief(_ context.Context, name string, slots map[string]string) (Brief, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.slots == nil {
		f.slots = map[string][]map[string]string{}
	}
	f.slots[name] = append(f.slots[name], slots)
	return Brief{Agent: name, Parts: [5]string{"tools", "role " + name, "repo", "standards", "task"}, Tokens: 100}, nil
}

// slotsOf returns the slots of an agent's nth brief.
func (f *fakeBriefs) slotsOf(name string, n int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.slots[name]) {
		return nil
	}
	return f.slots[name][n]
}

// fakeCommit counts the Commit Handler calls.
type fakeCommit struct {
	mu    sync.Mutex
	saves []commit.SaveRequest
	pubs  []commit.Approval
	err   error
}

func (c *fakeCommit) Save(_ context.Context, r commit.SaveRequest) (commit.SaveResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saves = append(c.saves, r)
	return commit.SaveResult{Saved: true}, c.err
}

func (c *fakeCommit) Publish(_ context.Context, a commit.Approval) (commit.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pubs = append(c.pubs, a)
	return commit.Result{}, c.err
}

func (c *fakeCommit) saveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.saves)
}

func (c *fakeCommit) published() []commit.Approval {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.pubs)
}

// treeGit is a Git whose tree hash is the hash of the worktree's files.
type treeGit struct {
	mu       sync.Mutex
	adds     int
	status   string
	resets   int
	statuses []string // returned in order by status calls, then status
}

func (g *treeGit) TreeHash(_ context.Context, l LeaseRef) (string, error) {
	h := sha256.New()
	_ = filepath.WalkDir(l.Worktree, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(l.Worktree, p)
			h.Write([]byte(rel + "\x00"))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (g *treeGit) Diff(_ context.Context, l LeaseRef, _ []string) (string, error) {
	var b strings.Builder
	_ = filepath.WalkDir(l.Worktree, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(l.Worktree, p)
			body, _ := os.ReadFile(p)
			first, _, _ := strings.Cut(string(body), "\n")
			fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+%s\n", rel, rel, rel, first)
		}
		return nil
	})
	return b.String(), nil
}

func (g *treeGit) ReadOnly(_ context.Context, _ LeaseRef, command string, _ []string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if command == "status" && len(g.statuses) > 0 {
		s := g.statuses[0]
		g.statuses = g.statuses[1:]
		return s, nil
	}
	return g.status, nil
}

func (g *treeGit) ResetTracked(context.Context, LeaseRef) error {
	g.mu.Lock()
	g.resets++
	g.mu.Unlock()
	return nil
}

func (g *treeGit) AddWorktree(_ context.Context, l LeaseRef) error {
	g.mu.Lock()
	g.adds++
	g.mu.Unlock()
	return os.MkdirAll(l.Worktree, 0o755)
}

// fakeChecks returns a scripted check result per call.
type fakeChecks struct {
	mu    sync.Mutex
	calls int
	fail  []bool // call n fails when fail[n] is true
}

func (c *fakeChecks) Check(context.Context, agent.CommandRunner, agent.CheckInput) (agent.CheckResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.calls
	c.calls++
	if n < len(c.fail) && c.fail[n] {
		return agent.CheckResult{Steps: []agent.StepResult{{Step: agent.StepDoneWhen, Level: agent.LevelFail, Detail: []string{"task t1"}, Output: "FAIL: exact output"}}}, nil
	}
	return agent.CheckResult{Steps: []agent.StepResult{{Step: agent.StepDoneWhen, Level: agent.LevelPass}}}, nil
}

// model is the scripted model: each agent has a queue of steps, one per session started.
type model struct {
	mu     sync.Mutex
	steps  map[string][]func(s llm.Session) (llm.Stream, error)
	starts map[string]int
	resume func(s llm.Session, in llm.Input) (llm.Stream, error)
	t      *testing.T
}

// on queues steps for an agent.
func (m *model) on(name string, steps ...func(s llm.Session) (llm.Stream, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps[name] = append(m.steps[name], steps...)
}

// started returns how many sessions an agent has started.
func (m *model) started(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts[name]
}

// total returns how many sessions every agent has started.
func (m *model) total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, v := range m.starts {
		n += v
	}
	return n
}

// start runs the agent's next scripted step.
func (m *model) start(s llm.Session) (llm.Stream, error) {
	m.mu.Lock()
	m.starts[s.Agent]++
	q := m.steps[s.Agent]
	if len(q) == 0 {
		m.mu.Unlock()
		m.t.Errorf("no step left for a %s session", s.Agent)
		return nil, llm.ErrUnavailable
	}
	step := q[0]
	m.steps[s.Agent] = q[1:]
	m.mu.Unlock()
	return step(s)
}

// sayResult is a step that returns a finished stream with this result.
func sayResult(out string) func(llm.Session) (llm.Stream, error) {
	return func(llm.Session) (llm.Stream, error) {
		st := newFakeStream()
		st.ev <- resultEvent(out)
		return st, nil
	}
}

// building is a step that writes a file into the worktree, then returns this result.
func building(wt, file, body, out string) func(llm.Session) (llm.Stream, error) {
	return func(s llm.Session) (llm.Stream, error) {
		if err := os.WriteFile(filepath.Join(wt, file), []byte(body), 0o644); err != nil {
			return nil, err
		}
		return sayResult(out)(s)
	}
}

// Results the scripted agents return.
const (
	builderDone  = `{"status":"done","summary":"added the file"}`
	testerPass   = `{"items":[],"tasks":[{"task":"t1","confirmation":"confirmed"}]}`
	testerNoTask = `{"items":[],"tasks":[{"task":"t1","confirmation":"not_confirmed"}]}`
	reviewerMet  = `{"verdicts":[{"task":"t1","verdict":"met"}]}`
	reviewerNot  = `{"verdicts":[{"task":"t1","verdict":"not_met"}]}`
)

// pipeRig is one task group over fakes of every collaborator.
type pipeRig struct {
	t      *testing.T
	m      *Manager
	wt     string
	rt     *fakeRT
	briefs *fakeBriefs
	cm     *fakeCommit
	git    *treeGit
	model  *model
	lim    *fakeLimits
	checks *fakeChecks
	be     *hostBackend
	roles  map[string]Role
}

// rigOpts changes the defaults of newPipeRig.
type rigOpts struct {
	chain  []string
	limits Limits
	saved  *Progress
	items  []agent.Item
	mut    func(*GroupSpec, *Collaborators)
	starts map[llm.Link]func(llm.Session) (llm.Stream, error) // replaces the scripted model on a link
}

// newPipeRig builds a one-task group whose completion command is `true`.
func newPipeRig(t *testing.T, o rigOpts) *pipeRig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(wt, "gd")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chain := o.chain
	if chain == nil {
		chain = []string{string(llm.AnthropicAPI)}
	}
	perms := func(name string) Permissions {
		r, _ := agent.RoleByName(name)
		return Permissions{Tools: r.Tools, RunClasses: r.RunClasses, Guard: toRowIDs(r.Guard), Weight: "standard", Chain: chain}
	}
	spec := GroupSpec{
		Run: "run-1", Group: "tg-1", Files: []string{"src/**", "*.go"}, Saved: o.saved,
		Tasks:  []TaskSpec{{ID: "t1", Title: "add the file", DoneWhen: "build"}},
		Lease:  LeaseRef{Branch: "komodo/tg-1", Worktree: wt, GitDir: filepath.Join(wt, "gd"), Fence: 3},
		Agents: map[string]Permissions{"builder": perms("builder"), "tester": perms("tester"), "reviewer": perms("reviewer")},
		Limits: o.limits,
	}
	if spec.Limits.SessionRetries == 0 {
		spec.Limits.SessionRetries = 1
	}
	mdl := &model{steps: map[string][]func(llm.Session) (llm.Stream, error){}, starts: map[string]int{}, t: t}
	links := map[llm.Link]llm.Provider{}
	for _, l := range append([]string{string(llm.AnthropicAPI)}, chain...) {
		p := subLink(llm.Link(l))
		p.start = mdl.start
		if f := o.starts[llm.Link(l)]; f != nil {
			p.start = f
		}
		p.resume = func(s llm.Session, in llm.Input) (llm.Stream, error) {
			if mdl.resume == nil {
				return nil, llm.ErrUnavailable
			}
			return mdl.resume(s, in)
		}
		links[llm.Link(l)] = p
	}
	lim := newLimits()
	r := &pipeRig{t: t, wt: wt, rt: &fakeRT{}, briefs: &fakeBriefs{}, cm: &fakeCommit{}, git: &treeGit{}, model: mdl, lim: lim,
		checks: &fakeChecks{}, be: &hostBackend{}}
	router := &Router{Client: llm.New(links), Quota: lim, Paid: "never", Now: func() time.Time { return t0 }}
	c := Collaborators{
		Runtime: r.rt, Briefs: r.briefs, Git: r.git, Commit: r.cm, Router: router, Client: router.Client, Limits: lim,
		Sandboxes: sandbox.NewManager(map[string]sandbox.Backend{"builder": r.be, "tester": r.be}, nil),
		Checks:    r.checks, Commands: map[string][]string{"build": {"true"}, "test": {"true"}, "fail": {"false"}},
		Meta: GroupMeta{Repo: "repo-1", Epic: "ep-1", EpicGroups: []string{"tg-1"}, Goal: "add the file"},
		Plan: SandboxPlan{Temp: t.TempDir()}, SessionsDir: t.TempDir(), Base: "base0", Language: "go",
		Tick: 5 * time.Millisecond, Ladder: [2]time.Duration{20 * time.Millisecond, 20 * time.Millisecond},
		ResumeWait: func(int) time.Duration { return time.Minute },
	}
	if o.mut != nil {
		o.mut(&spec, &c)
	}
	var m *Manager
	roles, err := NewRoles(RoleConfig{Group: "tg-1", Tasks: spec.Tasks, Items: o.items, Worktree: spec.Lease.Worktree,
		Files: func() []string { return m.Files() }, Runner: func() agent.CommandRunner { return m.Runner("tester") }})
	if err != nil {
		t.Fatal(err)
	}
	c.Roles = roles
	m, err = New(spec, c)
	if err != nil {
		t.Fatal(err)
	}
	r.m, r.roles, r.wt = m, roles, spec.Lease.Worktree
	return r
}

// toRowIDs converts row names.
func toRowIDs(s []string) []RowID {
	out := make([]RowID, len(s))
	for i, v := range s {
		out[i] = RowID(v)
	}
	return out
}

// run calls Run and waits for it to end, failing the test after ten seconds.
func (r *pipeRig) run() error {
	r.t.Helper()
	return r.within(func() error { return r.m.Run(context.Background()) })
}

// within runs f and fails the test when it takes longer than ten seconds.
func (r *pipeRig) within(f func() error) error {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		r.t.Fatal("the call did not return in 10 seconds")
		return nil
	}
}

// reachCleanup runs the group until it waits in cleanup, then closes it.
func (r *pipeRig) reachCleanup() {
	r.t.Helper()
	r.reachCleanupWith(r.m.Run)
}

// reachCleanupWith runs the call until the group waits in cleanup, then closes it.
func (r *pipeRig) reachCleanupWith(call func(context.Context) error) {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- call(context.Background()) }()
	deadline := time.After(10 * time.Second)
	for r.m.Progress().Stage != state.StageCleanup {
		select {
		case err := <-done:
			r.t.Fatalf("the call ended before cleanup: %v (stages %v)", err, r.rt.stagesSeen())
		case <-deadline:
			r.t.Fatalf("the group never reached cleanup (stages %v)", r.rt.stagesSeen())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := r.m.CloseTaskGroup(context.Background()); err != nil {
		r.t.Fatal(err)
	}
	if err := <-done; err != nil {
		r.t.Fatal(err)
	}
}

// nextBrief returns a string field of the nth brief an agent received.
func (r *pipeRig) slot(name string, n int, key string) string {
	return r.briefs.slotsOf(name, n)[key]
}

// mustJSON encodes v.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
