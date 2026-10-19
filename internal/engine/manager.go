package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// codeError is a refusal with the reply code the control socket answers with.
type codeError struct{ code, detail string }

// Error returns the sentence for a person.
func (e *codeError) Error() string { return e.detail }

// run is one execution of the task groups a run-start event names; its fields are guarded by the manager's lock.
type run struct {
	id         string
	start      bus.RunStart
	limits     Limits
	configHash string
	ctx        context.Context
	cancel     context.CancelFunc
	store      StateStore
	cache      cache.Handle
	specs      map[string]GroupSpec
	order      []string
	groups     map[string]*group
	done       map[string]bool   // groups that ended with outcome complete
	finished   map[string]bool   // groups that are Done with any outcome
	lost       map[string]bool   // groups whose lease another run holds, which never started
	failed     map[string]bool   // groups that failed for good, which no run continues
	depBlocked map[string]string // groups that wait on an abandoned dependency
	landing    map[string]LandingPlan
	first      sync.Once
	firstCh    chan struct{}
	ended      bool
	stopping   bool
	stalled    bool
}

// firstSave marks that a group record of the run was written.
func (r *run) firstSave() { r.first.Do(func() { close(r.firstCh) }) }

// landingFor returns the landing plan of an epic.
func (r *run) landingFor(epic string) (LandingPlan, bool) {
	p, ok := r.landing[epic]
	return p, ok
}

// otherFiles returns the file globs of every other group in the run.
func (r *run) otherFiles(id string) []string {
	var out []string
	for gid, s := range r.specs {
		if gid != id {
			out = append(out, s.Files...)
		}
	}
	return out
}

// repoCache is one repository's cache and the number of runs that use it.
type repoCache struct {
	c    *cache.Cache
	refs int
}

// Manager is the Runtime Manager: it runs every live run of the engine.
type Manager struct {
	d   Deps
	clk Clock
	t   Timing

	mu       sync.Mutex
	ctx      context.Context
	runs     map[string]*run
	groups   map[string]*group
	starting map[string]bool
	repos    map[string]*repoCache
	expect   map[string]chan error
	conts    map[string]chan error
	beats    map[string]time.Time
	pool     *Pool
	chg      chan struct{}
	stopped  bool
	faults   *fault.Handler // reports a panic of a goroutine the package starts
	openMu   sync.Mutex     // makes the check and the open of a repository cache one step
	startMu  sync.Mutex     // makes the live-group check and the group registration of a run start one step
	ready    chan struct{}  // closed once Run has subscribed to the command events
	readyOne sync.Once
	live     sync.WaitGroup // goroutines started through spawn
}

// killGrace is how long a stop waits for a Task Manager after its process tree was killed.
const killGrace = time.Second

// protectedPaths are the paths no scope request may widen to.
var protectedPaths = []string{
	".git",
	".git/**",
	".komodo/**",
	".claude/**",
	".env",
	".env.*",
	"**/.env",
	"**/.env.*",
	"**/*.pem",
	"**/*.key",
	"**/id_rsa*", "**/id_ed25519*", "**/secrets/**", "**/*credentials*",
}

// New wires a Runtime Manager from the managers it needs.
func New(d Deps) *Manager {
	return &Manager{
		d: d, clk: clockOf(d.Clock), t: d.Timing.filled(), ctx: context.Background(),
		runs:     map[string]*run{},
		groups:   map[string]*group{},
		starting: map[string]bool{},
		repos:    map[string]*repoCache{},
		expect:   map[string]chan error{},
		conts:    map[string]chan error{},
		beats:    map[string]time.Time{},
		chg:      make(chan struct{}),
		ready:    make(chan struct{}),
		faults:   faultsOf(d.Faults),
	}
}

// Run subscribes to run-start, run-stop and run-continue and serves them until ctx ends.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	sub, err := m.d.Bus.Subscribe("runtime", bus.TypeRunStart, bus.TypeRunStop, bus.TypeRunContinue)
	if err != nil {
		return fmt.Errorf("subscribe to run commands: %w", err)
	}
	defer sub.Close()

	m.readyOne.Do(func() { close(m.ready) })
	m.spawn(func() { m.watch(ctx) })

	for {
		select {
		case e, ok := <-sub.Events:
			if !ok {
				return nil
			}
			m.dispatch(ctx, e)
		case <-ctx.Done():
			return nil
		}
	}
}

// Expect registers interest in the outcome of a command before it is published; the channel receives the error or nil.
func (m *Manager) Expect(request string) <-chan error {
	ch := make(chan error, 1)
	m.mu.Lock()
	m.expect[request] = ch
	m.mu.Unlock()
	return ch
}

// Changes returns a channel that closes at the next change to a run or a group.
func (m *Manager) Changes() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chg
}

// Live returns the number of runs that are live or starting.
func (m *Manager) Live() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs) + len(m.starting)
}

// OpenRepos returns the repositories that have an open run.
func (m *Manager) OpenRepos() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, r := range m.runs {
		out[r.start.Repo] = true
	}
	return out
}

// Report applies one Input a reader made from a Task Manager's message; the answer arrives later as an Order.
func (m *Manager) Report(ctx context.Context, in Input) error {
	m.mu.Lock()
	g := m.groups[in.Group]
	m.mu.Unlock()
	if g == nil {
		return fmt.Errorf("group %s is not live in this engine", in.Group)
	}
	if in.Kind == InScopeAsk {
		in = m.decorateScopeAsk(g, in)
	}

	select {
	case g.in <- in:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-g.done:
		return fmt.Errorf("group %s has ended", g.spec.Label)
	}
}

// Beat records that a core loop is alive.
func (m *Manager) Beat(loop string) { m.beat(loop) }

// CheckBeats restarts every core loop silent for LoopSilentFor.
func (m *Manager) CheckBeats() {
	now := m.clk.Now()
	m.mu.Lock()

	var silent []string
	for loop, at := range m.beats {
		if now.Sub(at) >= m.t.LoopSilentFor {
			silent = append(silent, loop)
			m.beats[loop] = now
		}
	}
	m.mu.Unlock()

	for _, loop := range silent {
		if id, ok := strings.CutPrefix(loop, "task:"); ok {
			m.spawn(func() { m.restartTM(id) })
		} else if m.d.RestartLoop != nil {
			m.d.RestartLoop(loop)
		}
	}
}

// dispatch hands one command event to its handler on its own goroutine.
func (m *Manager) dispatch(ctx context.Context, e bus.Event) {
	var h func()
	switch d := e.Data.(type) {
	case *bus.RunStart:
		h = func() { m.settle(string(d.Request), m.startRun(ctx, e, d)) }
	case *bus.RunStop:
		h = func() { m.settle(string(d.Request), m.stopTarget(ctx, d.Target)) }
	case *bus.RunContinue:
		h = func() { m.settle(string(d.Request), m.continueTarget(ctx, d, string(e.Run))) }
	default:
		return
	}

	m.spawn(h)
}

// settle reports a command's outcome to its waiter and, for a refusal, to the event bus.
func (m *Manager) settle(request string, err error) {
	if request == "" {
		return
	}

	var ev bus.Payload = &bus.RequestAccepted{RequestRef: bus.RequestRef{Request: bus.ID(request)}}
	if err != nil {
		ev = &bus.RequestRefused{RequestRef: bus.RequestRef{Request: bus.ID(request)}, Why: err.Error()}
	}

	_ = m.d.Bus.Publish(Event{Type: bus.TypeRunStatus, Subtype: ev.EventKey().Subtype, Data: ev})
	m.mu.Lock()
	ch := m.expect[request]
	delete(m.expect, request)
	m.mu.Unlock()
	if ch != nil {
		ch <- err
	}
}

// startRun reads the plan, makes ready the epic branches and the cache, and starts the run's groups.
func (m *Manager) startRun(ctx context.Context, e bus.Event, rs *bus.RunStart) error {
	id := string(e.Run)

	m.mu.Lock()
	if m.runs[id] != nil || m.starting[id] {
		m.mu.Unlock()
		return nil
	}

	m.starting[id] = true

	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		m.signalLocked()
		m.mu.Unlock()
	}()

	m.startMu.Lock()
	defer m.startMu.Unlock()

	req := string(rs.Request)
	fail := func(format string, a ...any) error {
		return m.refuse(id, rs.Target, req, fmt.Sprintf(format, a...))
	}
	if len(rs.Groups) == 0 {
		return fail("the run names no groups")
	}

	l, err := LoadLimits(m.d.Config)
	if err != nil {
		return fail("read the limits: %v", err)
	}

	specs, err := m.d.Plans.Specs(ctx, rs.Root, rs.Groups)
	if err != nil {
		return fail("read the task lists: %v", err)
	}

	byLabel := map[string]GroupSpec{}
	for _, s := range specs {
		byLabel[s.Label] = s
	}

	for _, label := range rs.Groups {
		if _, ok := byLabel[label]; !ok {
			why := fmt.Sprintf("no task group %s in the plan", label)
			m.publishRefused(id, rs.Target, req, why)
			return &codeError{CodeUnknownTarget, why}
		}
	}
	if err := CheckPlan(specs); err != nil {
		return fail("%v", err)
	}

	store, err := m.d.States(rs.CommonDir)
	if err != nil {
		return fail("open the state of %s: %v", rs.Repo, err)
	}

	r := &run{
		id:         id,
		start:      *rs,
		limits:     l,
		configHash: configHashOf(l),
		store:      store,
		specs:      map[string]GroupSpec{},
		groups:     map[string]*group{},
		done:       map[string]bool{},
		finished:   map[string]bool{},
		lost:       map[string]bool{},
		failed:     map[string]bool{},
		depBlocked: map[string]string{},
		landing:    map[string]LandingPlan{},
		firstCh:    make(chan struct{}),
	}
	saved := map[string]state.Record{}

	var forward []*group

	for _, s := range specs {
		m.mu.Lock()
		live := m.groups[s.ID]
		m.mu.Unlock()
		if live != nil {
			forward = append(forward, live)
			continue
		}

		r.specs[s.ID] = s
		r.order = append(r.order, s.ID)

		rec, err := store.Load(ctx, s.ID)
		switch {
		case errors.Is(err, state.ErrNotFound):
		case err != nil:
			return fail("read the saved state of %s: %v", s.Label, err)
		case rec.State.Stage == StageDone:
			r.finished[s.ID] = true
			r.done[s.ID] = rec.State.Outcome == "complete"
			if rec.State.Outcome == "failed" {
				r.failed[s.ID] = true
				if len(rs.Groups) == 1 && rs.Groups[0] == s.Label {
					why := s.Label + " failed for good: plan it again to run it fresh"
					m.publishRefused(id, rs.Target, req, why)
					return &codeError{CodeGroupFailed, why}
				}
			}
		default:
			saved[s.ID] = rec
		}
	}

	var pending []pendingContinue

	for _, g := range forward {
		pending = append(pending, m.queueContinue(g, "run-"+id+"-"+g.spec.ID))
	}
	if len(r.specs) == 0 {
		return m.awaitContinues(ctx, pending)
	}

	for _, s := range r.specs {
		for _, d := range s.Depends {
			if _, in := r.specs[d]; in {
				continue
			}
			rec, err := store.Load(ctx, d)
			if err != nil || rec.State.Stage != StageDone || rec.State.Outcome != "complete" {
				return fail("dependency %s of %s is outside the run and is not Done", d, s.Label)
			}
		}
	}

	if err := m.prepareEpics(ctx, rs, r, saved); err != nil {
		return err
	}
	if err := m.openCache(ctx, r); err != nil {
		return fail("open the cache: %v", err)
	}

	plans := BuildLanding(slices.Collect(func(yield func(GroupSpec) bool) {
		for _, id := range r.order {
			if !yield(r.specs[id]) {
				return
			}
		}
	}))
	for _, p := range plans {
		r.landing[p.Epic] = p
	}

	m.mu.Lock()
	if m.pool == nil {
		m.pool = NewPool(l, m.clk, m.t.PoolHalveCooldown)
	}

	r.ctx, r.cancel = context.WithCancel(m.ctx)
	m.runs[id] = r
	m.signalLocked()
	m.mu.Unlock()

	ids := make([]bus.ID, 0, len(r.order))
	for _, gid := range r.order {
		ids = append(ids, bus.ID(gid))
	}

	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunStatus,
		Subtype: "run.start",
		Run:     bus.ID(id),
		Data:    &bus.RunStartedFact{Groups: ids, ConfigHash: r.configHash, Version: m.d.Version},
	})
	m.mu.Lock()
	for gid, rec := range saved {
		rec := rec
		g := m.newGroup(r, r.specs[gid])
		r.groups[gid] = g
		m.groups[gid] = g
		m.spawn(func() { g.main(&rec) })
		if rec.State.Stage == StagePaused || rec.State.Stage == StageBlocked {
			req := "run-" + id + "-" + gid
			ch := make(chan error, 1)
			m.conts[req] = ch
			pending = append(pending, pendingContinue{g: g, ch: ch, req: req})
			g.enqueue(Input{Kind: InContinue, Request: req})
		}
	}
	if len(saved) == 0 && len(r.finished) == 0 {
		r.firstSave()
	}
	m.mu.Unlock()
	m.schedule()
	m.mu.Lock()
	m.checkLocked(r)
	m.mu.Unlock()
	return m.awaitContinues(ctx, pending)
}

// signalLocked wakes everything that waits on Changes.
func (m *Manager) signalLocked() {
	close(m.chg)
	m.chg = make(chan struct{})
}

// refuse publishes an error / run-refused event for the run and returns the refusal.
func (m *Manager) refuse(run, target, request, why string) error {
	m.publishRefused(run, target, request, why)
	return &codeError{CodeRunRefused, why}
}

// publishRefused publishes the error / run-refused event of a run, so a plan waiting on that run learns it did not start.
func (m *Manager) publishRefused(run, target, request, why string) {
	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeError,
		Subtype: "run-refused",
		Run:     bus.ID(run),
		Data:    &RunRefused{Target: target, Why: why, Request: request},
	})
}

// prepareEpics makes sure each epic's branch exists and holds the task lists before any group branch is cut.
func (m *Manager) prepareEpics(ctx context.Context, rs *bus.RunStart, r *run, saved map[string]state.Record) error {
	seen := map[string]bool{}

	for _, gid := range r.order {
		s := r.specs[gid]
		if _, resumed := saved[gid]; resumed || r.finished[gid] || seen[s.Epic] {
			continue
		}

		seen[s.Epic] = true
		branch := "komodo/" + s.Epic
		ok, err := m.d.Git.BranchExists(ctx, rs.Root, branch)
		if err != nil {
			return m.refuse(r.id, rs.Target, string(rs.Request), fmt.Sprintf("look for branch %s: %v", branch, err))
		}
		if ok {
			continue
		}

		base := m.d.BaseBranch
		if base == "" {
			base = "main"
		}
		if err := m.d.Git.CreateBranch(ctx, rs.Root, branch, base); err != nil {
			// Another writer created the branch since the check above; it is there, which is all this step needs.
			if again, _ := m.d.Git.BranchExists(ctx, rs.Root, branch); again {
				continue
			}

			return m.refuse(r.id, rs.Target, string(rs.Request), fmt.Sprintf("create branch %s: %v", branch, err))
		}

		if err := m.d.Commits.SaveLists(
			ctx,
			commit.ListsRequest{
				Repo:      rs.Repo,
				Root:      rs.Root,
				CommonDir: rs.CommonDir,
				Epic:      s.Epic,
			},
		); err != nil {
			return m.refuse(r.id, rs.Target, string(rs.Request), fmt.Sprintf("commit the task lists of %s: %v", s.Epic, err))
		}
	}

	return nil
}

// openCache opens the repository's cache at its first run and hands the handle to the Context Manager.
func (m *Manager) openCache(ctx context.Context, r *run) error {
	m.openMu.Lock()
	defer m.openMu.Unlock()

	repo := r.start.Repo

	m.mu.Lock()
	rc := m.repos[repo]
	m.mu.Unlock()

	if rc == nil {
		c, err := m.d.Caches.Open(ctx, repo)
		if err != nil {
			return err
		}
		rc = &repoCache{c: c}
		m.d.Contexts.UseCache(repo, c.Handle())
		m.mu.Lock()
		m.repos[repo] = rc
		m.mu.Unlock()
	}

	m.mu.Lock()
	rc.refs++
	r.cache = rc.c.Handle()
	m.mu.Unlock()
	return nil
}

// spawn runs fn on a guarded goroutine that wait can wait for.
func (m *Manager) spawn(fn func()) {
	m.live.Add(1)

	run := func() {
		defer m.live.Done()
		fn()
	}
	if m.d.Go != nil {
		m.d.Go(run)
		return
	}

	guarded(m.faults, run)
}

// wait waits up to d for every spawned goroutine to end; it reports false when some are still running.
func (m *Manager) wait(d time.Duration) bool {
	done := make(chan struct{})
	guarded(m.faults, func() {
		m.live.Wait()
		close(done)
	})

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// schedule starts ready groups, best first, while the pool has a free slot.
func (m *Manager) schedule() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for !m.stopped {
		var all []Candidate

		owner := map[string]*run{}

		for _, r := range m.runs {
			if r.ended || r.stopping {
				continue
			}

			specs := make([]GroupSpec, 0, len(r.order))
			for _, id := range r.order {
				specs = append(specs, r.specs[id])
			}

			for _, c := range Candidates(specs, r.done) {
				id := c.Spec.ID
				if r.groups[id] != nil || r.finished[id] || r.lost[id] || r.depBlocked[id] != "" {
					continue
				}
				if m.groups[id] != nil {
					// another run's loop owns the group, so this run never starts it
					r.lost[id] = true
					continue
				}

				all = append(all, c)
				owner[id] = r
			}
		}

		c, ok := Pick(all)
		if !ok || m.pool == nil || !m.pool.TryAcquire() {
			return
		}

		r := owner[c.Spec.ID]
		g := m.newGroup(r, c.Spec)
		g.hasSlot = true
		r.groups[c.Spec.ID] = g
		m.groups[c.Spec.ID] = g
		m.spawn(func() { g.main(nil) })
	}
}

// checkLocked ends the run when every group is Done and reports a stall when only blocked groups remain.
func (m *Manager) checkLocked(r *run) {
	if r.ended || r.stopping || r.ctx == nil {
		return
	}

	var (
		active, paused    int
		stuck             []StalledGroup
		doneIDs, otherIDs []bus.ID
	)

	for _, id := range r.order {
		s := r.specs[id]
		if r.finished[id] {
			if r.done[id] {
				doneIDs = append(doneIDs, bus.ID(id))
			} else {
				otherIDs = append(otherIDs, bus.ID(id))
				if r.failed[id] {
					stuck = append(stuck, StalledGroup{Group: s.Label, Reason: "failed"})
				}
			}
			continue
		}

		g := r.groups[id]
		if g == nil {
			switch {
			case r.lost[id]:
				stuck = append(stuck, StalledGroup{Group: s.Label, Reason: "lease_lost"})
			case r.depBlocked[id] != "":
				stuck = append(
					stuck,
					StalledGroup{
						Group:  s.Label,
						Reason: string(state.ReasonDependency),
						On:     r.depBlocked[id],
					},
				)
			}
			continue
		}

		g.mu.Lock()
		snap, dead := g.snap, g.dead
		g.mu.Unlock()

		switch {
		case dead:
			stuck = append(stuck, StalledGroup{Group: s.Label, Reason: string(state.ReasonCrash)})
		case snap.Stage == StageBlocked:
			stuck = append(stuck, StalledGroup{Group: s.Label, Reason: string(snap.Block.Reason)})
		case snap.Stage == StagePaused:
			paused++
		default:
			active++
		}
	}

	if len(doneIDs)+len(otherIDs) == len(r.order) {
		r.ended = true
		m.endRunLocked(r, &bus.RunDone{Done: orEmpty(doneIDs), Blocked: orEmpty(otherIDs)}, "done")
		return
	}
	if active > 0 || paused > 0 || len(stuck) == 0 {
		r.stalled = false
		return
	}

	waiting := m.waitingLocked(r)
	if waiting {
		return
	}

	if !r.stalled {
		r.stalled = true
		for _, id := range r.order {
			if g := r.groups[id]; g == nil && !r.finished[id] && !r.lost[id] && r.depBlocked[id] == "" {
				stuck = append(
					stuck,
					StalledGroup{
						Group:  r.specs[id].Label,
						Reason: string(state.ReasonDependency),
						On:     m.firstUnfinishedDep(r, id),
					},
				)
			}
		}

		_ = m.d.Bus.Publish(Event{
			Type:    bus.TypeRunFailure,
			Subtype: "stalled",
			Run:     bus.ID(r.id),
			Data:    &Stalled{Groups: stuck},
		})
	}
}

// endRunLocked publishes the run's end and frees what the run held.
func (m *Manager) endRunLocked(r *run, payload bus.Payload, sub string) {
	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunComplete,
		Subtype: bus.Subtype(sub),
		Run:     bus.ID(r.id),
		Data:    payload,
	})
	delete(m.runs, r.id)

	for id, g := range r.groups {
		if m.groups[id] == g {
			delete(m.groups, id)
		}
	}
	if rc := m.repos[r.start.Repo]; rc != nil {
		rc.refs--
	}
	if m.d.Resource != nil {
		m.d.Resource.EndRun(r.id)
	}
	if r.cancel != nil {
		m.spawn(r.cancel)
	}

	m.signalLocked()
	if len(m.runs) == 0 && len(m.starting) == 0 {
		m.closeCachesLocked()
	}
}

// closeCachesLocked closes the cache of every repository whose last run ended.
func (m *Manager) closeCachesLocked() {
	open := false
	for repo, rc := range m.repos {
		if rc.refs <= 0 {
			delete(m.repos, repo)
		} else {
			open = true
		}
	}
	if !open && m.d.Caches != nil {
		_ = m.d.Caches.Close(m.ctx)
	}
}

// waitingLocked reports whether a not-started group could still start without anyone's answer.
func (m *Manager) waitingLocked(r *run) bool {
	specs := make([]GroupSpec, 0, len(r.order))
	for _, id := range r.order {
		specs = append(specs, r.specs[id])
	}

	for _, c := range Candidates(specs, r.done) {
		id := c.Spec.ID
		if r.groups[id] == nil && !r.finished[id] && !r.lost[id] && r.depBlocked[id] == "" {
			return true
		}
	}

	return false
}

// firstUnfinishedDep returns the label of the first dependency of a group that is not Done.
func (m *Manager) firstUnfinishedDep(r *run, id string) string {
	for _, d := range r.specs[id].Depends {
		if !r.done[d] {
			if s, ok := r.specs[d]; ok {
				return s.Label
			}
			return d
		}
	}
	return ""
}

// groupChanged reacts to a group's saved change: it checks the run and schedules what became ready.
func (m *Manager) groupChanged(g *group) {
	m.mu.Lock()
	m.signalLocked()
	m.mu.Unlock()
	m.schedule()
	m.mu.Lock()
	m.checkLocked(g.r)
	m.mu.Unlock()
}

// groupDone records a finished group in its run.
func (m *Manager) groupDone(g *group) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := g.r
	delete(m.beats, "task:"+g.spec.ID)
	r.finished[g.spec.ID] = true
	r.done[g.spec.ID] = g.st.Outcome == "complete"
	r.failed[g.spec.ID] = g.st.Outcome == "failed"
}

// leaseRefused records a group that never started because another run holds its lease.
func (m *Manager) leaseRefused(g *group, err error) {
	var held lease.HeldError

	holder := ""
	if errors.As(err, &held) {
		holder = held.Holder.Run
	}

	m.mu.Lock()
	g.r.lost[g.spec.ID] = true
	delete(g.r.groups, g.spec.ID)
	m.dropGroupLocked(g)
	m.mu.Unlock()

	_ = m.d.Bus.Publish(Event{
		Type:    bus.TypeRunFailure,
		Subtype: "lease.lost",
		Run:     bus.ID(g.r.id),
		Group:   bus.ID(g.spec.ID),
		Data: &bus.LeaseLost{
			GroupFacts: bus.GroupFacts{Stage: string(StageIntake)},
			Holder:     bus.ID(holder),
			Where:      "renew",
		},
	})
	if !errors.Is(err, lease.ErrLeased) {
		m.report(g, fmt.Errorf("take the lease of %s: %w", g.spec.Label, err))
	}
}

// dropGroupLocked removes a group that will not run from the engine-wide table, if the table still holds it, and drops its
// heartbeat.
func (m *Manager) dropGroupLocked(g *group) {
	if m.groups[g.spec.ID] == g {
		delete(m.groups, g.spec.ID)
	}
	delete(m.beats, "task:"+g.spec.ID)
}

// report publishes an error that the Runtime Manager cannot act on itself.
func (m *Manager) report(g *group, err error) {
	if m.d.Errors != nil {
		m.d.Errors(g.r.id, g.spec.ID, err)
	}
}

// byTarget returns the live groups a label, group id or epic names.
func (m *Manager) byTarget(target string) []*group {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []*group
	for _, g := range m.groups {
		if g.spec.ID == target || g.spec.Label == target || g.spec.Epic == target {
			out = append(out, g)
		}
	}

	slices.SortFunc(out, func(a, b *group) int {
		return strings.Compare(a.spec.Label, b.spec.Label)
	})

	return out
}

// decorateScopeAsk records the request and marks it for denial when a path is protected or belongs to another live group.
func (m *Manager) decorateScopeAsk(g *group, in Input) Input {
	a := decode[ScopeAsk](in.Data)

	m.mu.Lock()
	for _, p := range a.Paths {
		if anyGlob(protectedPaths, p) {
			a.Deny = "protected"
			break
		}
		for id, other := range g.r.specs {
			if id != g.spec.ID && !g.r.finished[id] && anyGlob(other.Files, p) {
				a.Deny = "other_group"
			}
		}
	}
	m.mu.Unlock()
	g.mu.Lock()
	g.ask, g.pendingEsc = &a, a.Escalation
	g.mu.Unlock()

	in.Data = mustJSON(a)
	return in
}

// beat stores the time of a loop's last beat.
func (m *Manager) beat(loop string) {
	now := m.clk.Now()
	m.mu.Lock()
	m.beats[loop] = now
	m.mu.Unlock()
}

// watch restarts a core loop that stopped beating, once every HeartbeatEvery.
func (m *Manager) watch(ctx context.Context) {
	for {
		t := make(chan struct{})
		m.clk.AfterFunc(m.t.HeartbeatEvery, func() { close(t) })
		select {
		case <-t:
			m.CheckBeats()
		case <-ctx.Done():
			return
		}
	}
}

// restartTM stops a silent Task Manager, waits for it to end (killing its process tree at the deadline), and lets the
// group's loop start a new one from the saved state.
func (m *Manager) restartTM(id string) {
	m.mu.Lock()
	g := m.groups[id]
	m.mu.Unlock()
	if g == nil {
		return
	}

	g.mu.Lock()
	old, done := g.cmds, g.tmDone
	g.mu.Unlock()

	if old != nil {
		select {
		case old <- stopCommand("restart"):
		default:
		}

		m.waitEnded(g, done)
		g.mu.Lock()
		if g.tmDone == done {
			g.cmds, g.tmDone = nil, nil
		}
		g.mu.Unlock()
	}

	g.enqueue(Input{Kind: InLoopRestart})
}

// waitEnded waits for a stopped Task Manager to end; at the shutdown deadline it kills the group's process tree and waits a
// short grace.
func (m *Manager) waitEnded(g *group, done chan struct{}) {
	if done == nil {
		return
	}

	t := time.NewTimer(m.t.ShutdownDeadline)
	defer t.Stop()

	select {
	case <-done:
		return
	case <-t.C:
	}
	if m.d.KillTree != nil {
		m.d.KillTree(g.r.id, g.spec.ID)
	}

	select {
	case <-done:
	case <-time.After(killGrace):
	}
}

// dropGroup is dropGroupLocked for a caller that does not hold the lock.
func (m *Manager) dropGroup(g *group) {
	m.mu.Lock()
	m.dropGroupLocked(g)
	m.mu.Unlock()
}

// configHashOf hashes the limits a run pinned.
func configHashOf(l Limits) string {
	sum := sha256.Sum256(mustJSON(l))
	return hex.EncodeToString(sum[:])
}

// orEmpty returns ids, or an empty list when nil.
func orEmpty(ids []bus.ID) []bus.ID {
	if ids == nil {
		return []bus.ID{}
	}
	return ids
}
