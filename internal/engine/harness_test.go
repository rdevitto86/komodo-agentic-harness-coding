package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// rigConfig serves numbers by key.
type rigConfig map[string]float64

// GetConfigItem returns the value of a key.
func (c rigConfig) GetConfigItem(key string) (config.Item, error) {
	v, ok := c[key]
	if !ok {
		return config.Item{}, fmt.Errorf("no key %s", key)
	}
	return config.Item{Key: key, Value: v}, nil
}

// rigAgents gives every agent one retry.
type rigAgents struct{}

// Retries returns 1.
func (rigAgents) Retries(string) (int, error) { return 1, nil }

// fakeStore is an in-memory State Manager.
type fakeStore struct {
	mu     sync.Mutex
	recs   map[string]state.Record
	onSave func(group string, s State)
	saves  atomic.Int64
}

// newStore returns an empty store.
func newStore() *fakeStore { return &fakeStore{recs: map[string]state.Record{}} }

// Load returns a group's record.
func (s *fakeStore) Load(_ context.Context, g string) (state.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[g]
	if !ok {
		return state.Record{}, state.ErrNotFound
	}
	return r, nil
}

// Create writes a first record.
func (s *fakeStore) Create(_ context.Context, g string, st State) (state.Record, error) {
	s.mu.Lock()
	if _, ok := s.recs[g]; ok {
		s.mu.Unlock()
		return state.Record{}, fmt.Errorf("record %s exists", g)
	}
	r := state.Record{Header: state.Header{Group: g, Rev: 1}, State: st}
	s.recs[g] = r
	s.mu.Unlock()
	s.saved(g, st)
	return r, nil
}

// Save writes a record at the revision after rev.
func (s *fakeStore) Save(_ context.Context, g string, st State, rev int) (state.Record, error) {
	s.mu.Lock()
	cur, ok := s.recs[g]
	if !ok || cur.Header.Rev != rev {
		s.mu.Unlock()
		return state.Record{}, fmt.Errorf("revision %d is stale", rev)
	}
	r := state.Record{Header: state.Header{Group: g, Rev: rev + 1}, State: st}
	s.recs[g] = r
	s.mu.Unlock()
	s.saved(g, st)
	return r, nil
}

// saved counts a write and runs the hook.
func (s *fakeStore) saved(g string, st State) {
	s.saves.Add(1)
	if s.onSave != nil {
		s.onSave(g, st)
	}
}

// get returns a group's saved state.
func (s *fakeStore) get(g string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recs[g].State
}

// put plants a record as a dead engine left it.
func (s *fakeStore) put(g string, st State, rev int) {
	s.mu.Lock()
	s.recs[g] = state.Record{Header: state.Header{Group: g, Rev: rev}, State: st}
	s.mu.Unlock()
}

// fakeLeases is an in-memory Lease Manager.
type fakeLeases struct {
	mu       sync.Mutex
	fence    int
	held     map[string]string // groups another run holds
	released map[string]int
	revoked  []string
	renewErr map[string]error
	renews   map[string]int
	order    *seqLog // shared log of lease and event order
}

// newLeases returns a lease fake.
func newLeases() *fakeLeases {
	return &fakeLeases{held: map[string]string{}, released: map[string]int{}, renews: map[string]int{}, renewErr: map[string]error{}}
}

// Grant takes a lease unless another run holds the group.
func (l *fakeLeases) Grant(_ context.Context, req lease.GrantRequest) (lease.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h, ok := l.held[req.Group]; ok {
		return lease.Lease{}, lease.HeldError{Holder: lease.Lease{Run: h, Group: req.Group}}
	}
	l.fence++
	return lease.Lease{Repo: req.Repo, Group: req.Group, Run: req.Run, Branch: req.Branch, Worktree: req.Worktree, Fence: lease.Fence(l.fence)}, nil
}

// Renew renews a lease or returns the error planted for the group.
func (l *fakeLeases) Renew(_ context.Context, x lease.Lease) (lease.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.renews[x.Group]++
	return x, l.renewErr[x.Group]
}

// Revoke records a revoke.
func (l *fakeLeases) Revoke(_ context.Context, _, group string, _ lease.RevokeReason) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revoked = append(l.revoked, group)
	l.order.add("revoke " + group)
	return nil
}

// Holder reports no holder.
func (l *fakeLeases) Holder(context.Context, string, string) (lease.Lease, bool, error) {
	return lease.Lease{}, false, nil
}

// IsOwner reports true.
func (l *fakeLeases) IsOwner(context.Context, lease.Lease) (bool, error) { return true, nil }

// Release counts a release.
func (l *fakeLeases) Release(_ context.Context, x lease.Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released[x.Group]++
	return nil
}

// releases returns how many times a group's lease was released.
func (l *fakeLeases) releases(g string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.released[g]
}

// fakeCaches opens one real cache per call and counts opens and closes.
type fakeCaches struct {
	dir    string
	opens  atomic.Int64
	closes atomic.Int64
	cache  *cache.Cache
}

// Open opens the cache of a repository.
func (c *fakeCaches) Open(_ context.Context, repo string) (*cache.Cache, error) {
	c.opens.Add(1)
	ca, err := cache.New(filepath.Join(c.dir, "cache"), "v", cache.Limits{})
	c.cache = ca
	return ca, err
}

// Close counts a close.
func (c *fakeCaches) Close(context.Context) error { c.closes.Add(1); return nil }

// fakeContexts records the cache handle and answers every context request.
type fakeContexts struct {
	mu     sync.Mutex
	used   []cache.Handle
	panics map[string]bool // groups whose context request panics
}

// UseCache records the handle.
func (c *fakeContexts) UseCache(_ string, h cache.Handle) {
	c.mu.Lock()
	c.used = append(c.used, h)
	c.mu.Unlock()
}

// GetContext returns an empty context, or panics for a planted group.
func (c *fakeContexts) GetContext(_ context.Context, _, group string) (json.RawMessage, error) {
	c.mu.Lock()
	p := c.panics[group]
	c.mu.Unlock()
	if p {
		panic("planted panic")
	}
	return json.RawMessage(`{}`), nil
}

// fakePlans serves specs by label and records the labels asked for.
type fakePlans struct {
	mu    sync.Mutex
	specs map[string]GroupSpec
	asked [][]string
}

// Specs returns the specs of the labels.
func (p *fakePlans) Specs(_ context.Context, labels []string) ([]GroupSpec, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, slices.Clone(labels))
	var out []GroupSpec
	for _, l := range labels {
		s, ok := p.specs[l]
		if !ok {
			return nil, fmt.Errorf("no group %s", l)
		}
		out = append(out, s)
	}
	return out, nil
}

// fakeGit records branch calls in one shared log.
type fakeGit struct {
	mu     sync.Mutex
	exists map[string]bool
	log    *seqLog
}

// BranchExists reports whether a branch was planted.
func (g *fakeGit) BranchExists(_ context.Context, _, branch string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.exists[branch], nil
}

// CreateBranch records the creation.
func (g *fakeGit) CreateBranch(_ context.Context, _, branch, base string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.log.add("create " + branch + " from " + base)
	return nil
}

// fakeCommits records the task-list commit in the shared log.
type fakeCommits struct {
	mu  sync.Mutex
	log *seqLog
}

// SaveLists records the call.
func (c *fakeCommits) SaveLists(_ context.Context, r commit.ListsRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.add("lists " + r.Epic)
	return nil
}

// fakeResource records the Resource Manager calls.
type fakeResource struct {
	mu      sync.Mutex
	granted []string
	runs    []string
	groups  []string
}

// GrantGroupTokens records a grant.
func (r *fakeResource) GrantGroupTokens(g string) { r.mu.Lock(); r.granted = append(r.granted, g); r.mu.Unlock() }

// EndRun records a run end.
func (r *fakeResource) EndRun(id string) { r.mu.Lock(); r.runs = append(r.runs, id); r.mu.Unlock() }

// EndGroup records a group end.
func (r *fakeResource) EndGroup(g string) { r.mu.Lock(); r.groups = append(r.groups, g) ; r.mu.Unlock() }

// tmEnv is what a fake Task Manager script sees.
type tmEnv struct {
	spec  GroupSpec
	cmds  chan task.Command
	out   chan task.Message
	store *fakeStore
	ctx   context.Context
	seq   int
}

// say sends a state report with the facts, stamped with the group's current stage.
func (e *tmEnv) say(facts ...task.Fact) {
	e.seq++
	st := e.store.get(e.spec.ID)
	m := task.Message{ID: fmt.Sprintf("%s-m%d", e.spec.ID, e.seq), Kind: task.MsgState,
		State: &task.StateReport{Group: e.spec.ID, Progress: task.Progress{Stage: workingOf(st)}, Facts: facts}}
	select {
	case e.out <- m:
	case <-e.ctx.Done():
	}
}

// send puts any message on the reply channel.
func (e *tmEnv) send(m task.Message) {
	e.seq++
	if m.ID == "" {
		m.ID = fmt.Sprintf("%s-m%d", e.spec.ID, e.seq)
	}
	select {
	case e.out <- m:
	case <-e.ctx.Done():
	}
}

// fact builds a fact of a subtype with data.
func fact(sub string, data any) task.Fact {
	return task.Fact{Subtype: sub, Data: mustJSON(data)}
}

// sessionFacts is a clean builder session on one task.
func sessionFacts(taskID string) []task.Fact {
	return []task.Fact{
		fact("session.start", map[string]any{"agent": "builder", "task": taskID, "session": "s-" + taskID, "link": "claude-cli", "model": "m", "stage": "build"}),
		fact("task.done", map[string]any{"task": taskID, "stage": "build"}),
		fact("session.end", map[string]any{"agent": "builder", "task": taskID, "session": "s-" + taskID, "link": "claude-cli", "reason": "done", "stage": "build"}),
	}
}

// happy is a Task Manager that completes every stage as soon as it is told to start.
func happy(e *tmEnv) {
	defer close(e.out)
	for {
		select {
		case c := <-e.cmds:
			if c.Kind == task.CmdStop {
				return
			}
			if c.Kind != task.CmdStart {
				continue
			}
			st := e.store.get(e.spec.ID)
			switch st.Stage {
			case StageIntake:
				e.say(fact("ready", nil))
			case StageBuild:
				var fs []task.Fact
				for _, t := range st.Tasks {
					if t.State != "done" {
						fs = append(fs, sessionFacts(t.ID)...)
					}
				}
				e.say(append(fs, fact("stage.done", StageDoneData{Stage: StageBuild}))...)
			case StageTestReview:
				e.say(fact("stage.done", StageDoneData{Stage: StageTestReview}))
			case StageCommit:
				e.say(fact("stage.done", StageDoneData{Stage: StageCommit}))
			case StageCleanup:
				e.send(task.Message{Kind: task.MsgEnded, Ended: &task.Ended{Result: "closed"}})
				return
			}
		case <-e.ctx.Done():
			return
		}
	}
}

// fakeTasks starts fake Task Managers, one script per group.
type fakeTasks struct {
	mu      sync.Mutex
	store   *fakeStore
	script  func(group string) func(*tmEnv)
	started map[string]int
	envs    map[string]*tmEnv
	cmdLog  map[string][]task.Command
	specs   map[string]GroupSpec
	failFor map[string]error
}

// Start starts a Task Manager goroutine and returns its channels.
func (f *fakeTasks) Start(ctx context.Context, spec GroupSpec, _ cache.Handle) (Channels, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failFor[spec.ID]; err != nil {
		return Channels{}, err
	}
	f.started[spec.ID]++
	f.specs[spec.ID] = spec
	cmds := make(chan task.Command, CommandBuffer)
	out := make(chan task.Message, CommandBuffer)
	env := &tmEnv{spec: spec, cmds: make(chan task.Command, CommandBuffer), out: out, store: f.store, ctx: ctx}
	f.envs[spec.ID] = env
	go func() {
		for c := range cmds {
			f.mu.Lock()
			f.cmdLog[spec.ID] = append(f.cmdLog[spec.ID], c)
			f.mu.Unlock()
			env.cmds <- c
		}
	}()
	script := happy
	if f.script != nil {
		if s := f.script(spec.ID); s != nil {
			script = s
		}
	}
	go script(env)
	return Channels{Commands: cmds, Replies: out}, nil
}

// startedCount returns how many Task Managers started for a group.
func (f *fakeTasks) startedCount(g string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[g]
}

// commands returns the commands a group's Task Manager received.
func (f *fakeTasks) commands(g string) []task.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmdLog[g])
}

// rig is a Runtime Manager wired to fakes.
type rig struct {
	t       *testing.T
	d       Deps
	m       *Manager
	bus     bus.Bus
	store   *fakeStore
	leases  *fakeLeases
	caches  *fakeCaches
	ctxs    *fakeContexts
	plans   *fakePlans
	git     *fakeGit
	commits *fakeCommits
	res     *fakeResource
	tasks   *fakeTasks
	clock   *FakeClock
	log     *seqLog
	home    string
	root    string
	ev      *evlog
	cancel  context.CancelFunc
	errs    atomic.Int64
	killed  atomic.Int64
	restart []string
	rmu     sync.Mutex
	noRun   bool // the test builds its own Manager through an Engine
}

// evlog collects every published event.
type evlog struct {
	mu  sync.Mutex
	all []bus.Event
}

// list returns the events so far.
func (l *evlog) list() []bus.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.all)
}

// count returns how many events have a type and subtype.
func (l *evlog) count(t bus.Type, sub string) int {
	n := 0
	for _, e := range l.list() {
		if e.Type == t && string(e.Subtype) == sub {
			n++
		}
	}
	return n
}

// rigOpt changes a rig before it starts.
type rigOpt func(*rig)

// groupSpecs builds specs from label, depends pairs; ids are "g-"+label.
func groupSpecs(epic string, defs ...[]string) map[string]GroupSpec {
	out := map[string]GroupSpec{}
	for i, d := range defs {
		s := GroupSpec{ID: "g-" + d[0], Label: d[0], Epic: epic, Tasks: []string{"t-" + d[0] + "-1"}, Files: []string{d[0] + "/**"}, Order: i}
		for _, dep := range d[1:] {
			s.Depends = append(s.Depends, "g-"+dep)
		}
		out[d[0]] = s
	}
	return out
}

// newRig starts a Runtime Manager over fakes; the groups are label then dependency labels.
func newRig(t *testing.T, defs [][]string, opts ...rigOpt) *rig {
	t.Helper()
	RegisterEvents()
	home := t.TempDir()
	machine, err := state.NewMachine(home, "engine")
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{log: &seqLog{}, t: t, home: home, root: filepath.Join(home, "repo"), store: newStore(), leases: newLeases(), ev: &evlog{}, clock: NewFakeClock(t0)}
	r.bus = bus.New(bus.Options{BufferEvents: 4096})
	r.caches = &fakeCaches{dir: home}
	r.ctxs = &fakeContexts{panics: map[string]bool{}}
	r.plans = &fakePlans{specs: groupSpecs("ep-1", defs...)}
	r.git = &fakeGit{exists: map[string]bool{}, log: r.log}
	r.commits = &fakeCommits{log: r.log}
	r.res = &fakeResource{}
	r.leases.order = r.log
	r.tasks = &fakeTasks{store: r.store, started: map[string]int{}, envs: map[string]*tmEnv{}, cmdLog: map[string][]task.Command{}, specs: map[string]GroupSpec{}, failFor: map[string]error{}}
	cfg := rigConfig{"limits.returns": 2, "limits.group_attempts": 2, "limits.pause_max_min": 360, "limits.group_wall_min": 60,
		"limits.group_tokens": 0, "limits.pool_min": 1, "limits.pool_max": 4, "sandbox.max_mem_gb": 2}
	r.d = Deps{Bus: r.bus, Config: cfg, Agents: rigAgents{}, States: func(string) (StateStore, error) { return r.store, nil }, Machine: machine,
		Leases: r.leases, Caches: r.caches, Contexts: r.ctxs, Tasks: r.tasks, Plans: r.plans, Git: r.git, Commits: r.commits, Resource: r.res,
		Home: home, BaseBranch: "main", Version: "test",
		Errors:      func(string, string, error) { r.errs.Add(1) },
		KillTree:    func(string, string) { r.killed.Add(1) },
		RestartLoop: func(l string) { r.rmu.Lock(); r.restart = append(r.restart, l); r.rmu.Unlock() },
		Chain:       func(string) []string { return []string{"claude-cli", "anthropic-api"} },
		Timing:      Timing{ShutdownDeadline: 300 * time.Millisecond},
	}
	for _, o := range opts {
		o(r)
	}
	sub, err := r.bus.Subscribe("test")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for e := range sub.Events {
			r.ev.mu.Lock()
			r.ev.all = append(r.ev.all, e)
			r.ev.mu.Unlock()
		}
	}()
	if r.noRun {
		t.Cleanup(func() { _ = r.bus.Close() })
		return r
	}
	r.m = New(r.d)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { _ = r.m.Run(ctx) }()
	<-r.m.Ready()
	t.Cleanup(func() {
		cancel()
		_ = r.bus.Close()
	})
	return r
}

// startRun publishes a run-start for the labels and waits for the Runtime Manager to settle it.
func (r *rig) startRun(runID string, labels ...string) error {
	r.t.Helper()
	req := "req-" + runID
	ch := r.m.Expect(req)
	rs := &bus.RunStart{Target: "ep-1", Groups: labels, Source: "cli", Repo: "github.com/acme/shop", Root: r.root, CommonDir: filepath.Join(r.root, ".git"), Request: bus.ID(req)}
	if err := r.bus.Publish(Event{Type: bus.TypeRunStart, Run: bus.ID(runID), Data: rs}); err != nil {
		r.t.Fatal(err)
	}
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		r.t.Fatal("the run start was not settled")
	}
	return nil
}

// until polls cond until it holds or the test fails.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// stageOf returns a group's saved stage.
func (r *rig) stageOf(label string) Stage { return r.store.get("g-" + label).Stage }

// waitStage waits for a group's saved stage.
func (r *rig) waitStage(label string, st Stage) {
	r.t.Helper()
	until(r.t, label+" in "+string(st), func() bool { return r.stageOf(label) == st })
}

// seqLog is an ordered, shared log of what the fakes were asked to do.
type seqLog struct {
	mu    sync.Mutex
	items []string
}

// add appends a line; a nil log drops it.
func (l *seqLog) add(s string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.items = append(l.items, s)
	l.mu.Unlock()
}

// get returns a copy of the lines.
func (l *seqLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.items)
}

var _ = os.Getpid

// renewCount returns how many times a group's lease was renewed.
func (l *fakeLeases) renewCount(g string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.renews[g]
}

// fakeCleaner records the groups discarded.
type fakeCleaner struct {
	mu   sync.Mutex
	gone []string
}

// Discard records a discard.
func (c *fakeCleaner) Discard(_ context.Context, _, group string) error {
	c.mu.Lock()
	c.gone = append(c.gone, group)
	c.mu.Unlock()
	return nil
}

// env returns the script environment of a group's Task Manager.
func (f *fakeTasks) env(g string) *tmEnv {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.envs[g]
}
