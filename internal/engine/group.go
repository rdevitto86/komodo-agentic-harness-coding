package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// group is one task group's loop: the only writer of the group's State.
type group struct {
	m      *Manager
	r      *run
	spec   GroupSpec
	ctx    context.Context
	cancel context.CancelFunc
	in     chan Input
	tick   chan struct{}
	done   chan struct{} // closed when the loop exits

	mu         sync.Mutex // guards the fields below, which other goroutines read
	snap       State
	epochs     map[Stage]uint32
	ask        *ScopeAsk
	pendingEsc string
	cmds       chan<- task.Command
	tmDone     chan struct{}
	dead       bool // the loop ended without a Done state

	// The fields below belong to the loop goroutine.
	st       State
	rev      int
	created  bool
	l        lease.Lease
	hasSlot  bool
	renewT   Timer
	timers   []Timer
	armed    pauseKey
	stopping atomic.Bool
}

// main is the group's goroutine: it starts or recovers the group, then takes one input at a time.
func (g *group) main(rec *state.Record) {
	defer close(g.done)
	defer g.stopTimers()
	defer g.giveBackSlot()

	ok := false

	g.safe(func() {
		if rec == nil {
			ok = g.begin()
		} else {
			ok = g.recoverInit(*rec)
		}
	})
	if !ok {
		g.markDead()
		return
	}

	g.armRenew()

	for {
		select {
		case in := <-g.in:
			g.safe(func() { g.handle(in) })
		case <-g.tick:
			g.safe(g.renew)
		case <-g.ctx.Done():
			return
		}
		if g.st.Stage == StageDone {
			g.drainTM()
			return
		}
	}
}

// stopTimers cancels every timer of the group.
func (g *group) stopTimers() {
	if g.renewT != nil {
		g.renewT.Stop()
	}
	for _, t := range g.timers {
		t.Stop()
	}
	g.timers = nil
}

// giveBackSlot returns the group's pool slot, if it holds one; the loop calls it on every way out.
func (g *group) giveBackSlot() {
	if g.hasSlot {
		g.hasSlot = false
		g.m.pool.Release()
	}
}

// safe runs fn and turns a panic into a group.crash input for this group only.
func (g *group) safe(fn func()) {
	defer func() {
		if p := recover(); p != nil {
			g.m.report(g, fmt.Errorf("group %s panicked: %v", g.spec.Label, p))
			g.enqueue(Input{Kind: InGroupCrash})
		}
	}()
	fn()
}

// enqueue puts an input on the group's channel; a full channel makes the sender wait and never drops it.
func (g *group) enqueue(in Input) {
	if in.Time.IsZero() {
		in.Time = g.now()
	}
	if in.Group == "" {
		in.Group = g.spec.ID
	}

	select {
	case g.in <- in:
	default:
		g.m.spawn(func() {
			select {
			case g.in <- in:
			case <-g.ctx.Done():
			}
		})
	}
}

// now returns the engine clock's time.
func (g *group) now() time.Time { return g.m.clk.Now() }

// begin takes the lease, writes the group's first record and performs its first order.
func (g *group) begin() bool {
	s := NewState(g.r.id, g.spec, g.r.configHash)
	s.LastEventAt = g.now()

	first := Next(s, Input{Kind: InGroupStart, Group: g.spec.ID, Time: g.now()}, g.r.limits)
	if first.Refused {
		return false
	}

	req := lease.GrantRequest{
		Repo:      g.r.start.Repo,
		Group:     g.spec.ID,
		Run:       g.r.id,
		Branch:    g.branchOf(),
		Worktree:  g.worktreeOf(),
		ClonePath: g.r.start.Root,
	}

	l, err := g.m.d.Leases.Grant(g.ctx, req)
	if err != nil {
		g.m.leaseRefused(g, err)
		return false
	}

	g.l = l
	st := first.State
	st.ActionsOpen = slices.DeleteFunc(st.ActionsOpen, func(a state.OpenAction) bool {
		return a.Kind == string(OrderGrantLease)
	})

	granted := Next(
		st,
		Input{
			Kind:  InLeaseGranted,
			Group: g.spec.ID,
			Time:  g.now(),
			Data: mustJSON(LeaseGrant{
				Fence:    uint64(l.Fence),
				Branch:   l.Branch,
				Worktree: l.Worktree,
			}),
		},
		g.r.limits,
	)
	if granted.Refused {
		_ = g.m.d.Leases.Release(g.ctx, l)
		return false
	}
	if err := g.save(granted.State); err != nil {
		g.m.report(g, fmt.Errorf("write first record of %s: %w", g.spec.Label, err))
		_ = g.m.d.Leases.Release(g.ctx, l)
		return false
	}

	g.r.firstSave()
	g.publish(first.Emit)
	g.publish(granted.Emit)
	g.after()
	g.perform(granted.Orders)
	return true
}

// branchOf returns the group's branch name.
func (g *group) branchOf() string { return "komodo/" + g.spec.Epic + "-" + g.spec.Label }

// worktreeOf returns the group's worktree path.
func (g *group) worktreeOf() string {
	return filepath.Join(g.m.d.Home, "wt", path.Base(g.r.start.Repo), g.spec.ID)
}

// save writes the new state in one write and keeps the record's revision.
func (g *group) save(ns State) error {
	var (
		rec state.Record
		err error
	)
	if !g.created {
		rec, err = g.r.store.Create(g.ctx, g.spec.ID, ns)
	} else {
		rec, err = g.r.store.Save(g.ctx, g.spec.ID, ns, g.rev)
	}
	if err != nil {
		return err
	}

	g.created, g.st, g.rev = true, rec.State, rec.Header.Rev
	g.mu.Lock()

	g.snap = g.st
	if working(g.st.Stage) {
		// Entering a stage forgets the epochs of the later stages, so a message of a stage reached again is not stale.
		for _, later := range pipelineStages[slices.Index(pipelineStages, g.st.Stage)+1:] {
			delete(g.epochs, later)
		}

		g.epochs[g.st.Stage] = g.st.Epoch
	}
	g.mu.Unlock()
	return nil
}

// publish sends events to the Event Bus after the write that produced them.
func (g *group) publish(es []Event) {
	for _, e := range es {
		if err := g.m.d.Bus.Publish(e); err != nil &&
			!errors.Is(err, bus.ErrBusSealed) &&
			!errors.Is(err, bus.ErrBusClosed) {
			g.m.report(g, err)
		}
	}
}

// after applies what the saved state asks of the engine around the group: the pool slot, the timers and the run's view.
func (g *group) after() {
	stage := g.st.Stage
	switch {
	case stage == StagePaused || stage == StageBlocked || stage == StageDone:
		if g.hasSlot {
			g.hasSlot = false
			g.m.pool.Release()
		}
	case working(stage) && !g.hasSlot:
		if g.m.pool.Acquire(g.ctx) == nil {
			g.hasSlot = true
		}
	}

	g.armTimers()
	if stage == StageDone {
		g.m.groupDone(g)
	}

	g.m.groupChanged(g)
}

// perform runs each order, then saves it as done; a rerun after a crash skips an order whose key is done.
func (g *group) perform(orders []Order) {
	for _, o := range orders {
		if g.stopping.Load() {
			return
		}
		if o.Kind == OrderEnterStage {
			g.fillContext(&o)
		}
		g.do(o)
		g.complete(o.Key)
	}
}

// fillContext adds the Context Manager's answer for the stage's agents to a stage order.
func (g *group) fillContext(o *Order) {
	var agents []string
	switch o.Stage {
	case StageBuild:
		agents = []string{"builder"}
	case StageTestReview:
		agents = []string{"tester", "reviewer"}
	default:
		return
	}

	d := decode[stageData](o.Data)
	if d.Agent != "" {
		agents = []string{d.Agent}
	}

	out := map[string]json.RawMessage{}

	for _, a := range agents {
		c, err := g.m.d.Contexts.GetContext(g.ctx, a, g.spec.ID)
		if err != nil {
			g.m.report(g, fmt.Errorf("context for %s of %s: %w", a, g.spec.Label, err))
			g.enqueue(Input{
				Kind: InFault,
				Data: mustJSON(FaultData{Reason: string(state.ReasonContext), Detail: err.Error()}),
			})
			continue
		}

		out[a] = c
	}

	o.Context = mustJSON(out)
}

// do performs one order.
func (g *group) do(o Order) {
	switch o.Kind {
	case OrderRelease:
		g.releaseLease()
		return
	case OrderDiscard:
		if g.m.d.Cleaner != nil {
			if err := g.m.d.Cleaner.Discard(
				context.WithoutCancel(g.ctx),
				g.r.start.Repo,
				g.r.start.Root,
				g.spec.ID,
			); err != nil {
				g.m.report(g, fmt.Errorf("discard %s: %w", g.spec.Label, err))
			}
		}

		return
	case OrderGrantLease:
		return
	case OrderCancel:
		if !g.tmUp() || g.holdsScopeRequest(o) {
			return
		}
	case OrderStartGroup, OrderEnterStage:
		if o.Kind == OrderEnterStage && o.Stage == StageCleanup && !g.tmUp() {
			return // the Cleanup Handler already closed the group and its Task Manager ended
		}
		if o.Kind == OrderEnterStage && o.Stage == StageCommit {
			g.attachLanding(&o)
		}
		if err := g.ensureTM(); err != nil {
			g.m.report(g, fmt.Errorf("start the Task Manager of %s: %w", g.spec.Label, err))
			g.enqueue(Input{
				Kind: InFault,
				Data: mustJSON(FaultData{Reason: string(state.ReasonSetup), Detail: err.Error()}),
			})
			return
		}
	case OrderScope:
		g.mu.Lock()
		if o.Re == "" {
			o.Re = g.pendingEsc
		}
		g.pendingEsc = ""
		g.mu.Unlock()
	}

	cmd, ok := commandFor(o, g.st)
	if !ok {
		return
	}

	cmd.Wake = wakeAt(cmd.Wake, g.now())
	g.send(cmd)
}

// releaseLease releases the group's lease and tells the Resource Manager the group ended.
func (g *group) releaseLease() {
	if err := g.m.d.Leases.Release(context.WithoutCancel(g.ctx), g.l); err != nil {
		g.m.report(g, fmt.Errorf("release the lease of %s: %w", g.spec.Label, err))
	}
	if g.m.d.Resource != nil {
		g.m.d.Resource.EndGroup(g.spec.ID)
	}
}

// tmUp reports whether a Task Manager goroutine is running for the group.
func (g *group) tmUp() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cmds != nil
}

// holdsScopeRequest reports whether a block's cancel meets a Task Manager that holds the group's open scope request, or is
// about to raise it after the session end that blocked the group; it stays up for the answer.
func (g *group) holdsScopeRequest(o Order) bool {
	if decode[map[string]string](o.Data)["cause"] != "block" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pendingEsc != "" || (g.snap.Stage == StageBlocked && g.snap.Block.Reason == state.ReasonNeedsScope)
}

// attachLanding records the epic's landing plan in a Commit order's data.
func (g *group) attachLanding(o *Order) {
	g.m.mu.Lock()
	p, ok := g.r.landingFor(g.spec.Epic)
	g.m.mu.Unlock()
	if ok {
		d := decode[stageData](o.Data)
		d.Landing = &p
		o.Data = mustJSON(d)
	}
}

// ensureTM starts the group's Task Manager goroutine when none runs.
func (g *group) ensureTM() error {
	if g.tmUp() {
		return nil
	}

	spec := g.spec
	spec.Run, spec.Repo, spec.Root = g.r.id, g.r.start.Repo, g.r.start.Root
	spec.Lease, spec.Limits = g.l, g.r.limits

	spec.OtherFiles = g.r.otherFiles(g.spec.ID)
	if g.created && g.st.Stage != StageIntake {
		s := cloneState(g.st)
		spec.Saved = &s
	}

	ch, err := g.m.d.Tasks.Start(g.ctx, spec, g.r.cache)
	if err != nil {
		return err
	}

	done := make(chan struct{})

	g.mu.Lock()
	g.cmds, g.tmDone = ch.Commands, done
	g.mu.Unlock()
	g.m.spawn(func() { g.read(ch.Replies, done) })
	return nil
}

// read turns every message of the Task Manager into inputs until its reply channel closes.
func (g *group) read(replies <-chan task.Message, done chan struct{}) {
	defer func() {
		g.mu.Lock()
		if g.tmDone == done {
			g.cmds, g.tmDone = nil, nil
		}
		g.mu.Unlock()
		close(done)
	}()

	for {
		select {
		case m, ok := <-replies:
			if !ok {
				return
			}

			g.m.beat("task:" + g.spec.ID)

			epochOf := func(st Stage) uint32 {
				g.mu.Lock()
				defer g.mu.Unlock()
				return g.epochs[st]
			}
			for _, in := range inputsFromMessage(g.spec.ID, m, epochOf, g.now()) {
				if err := g.m.Report(g.ctx, in); err != nil {
					g.m.report(g, err)
				}
			}
		case <-g.ctx.Done():
			return
		}
	}
}

// send puts a command on the Task Manager's channel.
func (g *group) send(cmd task.Command) {
	g.mu.Lock()
	ch, done := g.cmds, g.tmDone
	g.mu.Unlock()
	if ch == nil {
		return
	}

	select {
	case ch <- cmd:
	case <-done:
	case <-g.ctx.Done():
	}
}

// complete saves an order as done by removing it from the open actions.
func (g *group) complete(key string) {
	ns := cloneState(g.st)
	ns.ActionsOpen = slices.DeleteFunc(ns.ActionsOpen, func(a state.OpenAction) bool {
		return a.ID == key
	})
	if len(ns.ActionsOpen) == len(g.st.ActionsOpen) {
		return
	}
	if err := g.save(ns); err != nil {
		g.m.report(g, fmt.Errorf("save done order of %s: %w", g.spec.Label, err))
	}
}

// markDead records that the loop ended before the group was Done and lets the run notice.
func (g *group) markDead() {
	g.mu.Lock()
	g.dead = true
	g.mu.Unlock()
	g.giveBackSlot()
	g.m.dropGroup(g)
	g.m.groupChanged(g)
}

// armRenew schedules the next lease renewal.
func (g *group) armRenew() {
	if g.renewT != nil {
		g.renewT.Stop()
	}
	if g.st.Stage == StageDone {
		return
	}

	g.renewT = g.m.clk.AfterFunc(g.m.t.LeaseRenewEvery, func() {
		select {
		case g.tick <- struct{}{}:
		default:
		}
	})
}

// handle takes one input: decide, save, publish, then perform the orders.
func (g *group) handle(in Input) {
	if g.stopping.Load() || g.st.Stage == StageDone {
		return
	}

	if len(in.Data) > 0 && !json.Valid(in.Data) {
		in = Input{
			Kind:  InFault,
			Group: in.Group,
			Time:  in.Time,
			Data: mustJSON(FaultData{
				Reason: string(state.ReasonContract),
				Detail: "the " + string(in.Kind) + " data is not valid JSON",
			}),
		}
	}

	res := Next(g.st, in, g.r.limits)
	switch {
	case res.Dup || res.Stale:
		return
	case res.Refused:
		g.publish(res.Emit)
		return
	}
	for _, d := range res.Dropped {
		g.m.report(g, errors.New(d))
	}
	if err := g.save(res.State); err != nil {
		g.saveFailed(err)
		return
	}

	g.publish(res.Emit)
	g.sessionSignals(in)
	g.after()
	g.perform(res.Orders)
}

// saveFailed blocks the group state_invalid after the State Manager refused a write.
func (g *group) saveFailed(err error) {
	g.m.report(g, fmt.Errorf("save %s: %w", g.spec.Label, err))

	res := Next(
		g.st,
		Input{
			Kind:  InFault,
			Group: g.spec.ID,
			Time:  g.now(),
			Data: mustJSON(FaultData{
				Reason: string(state.ReasonStateInvalid),
				Detail: err.Error(),
			}),
		},
		g.r.limits,
	)
	if res.Refused || g.save(res.State) != nil {
		g.mu.Lock()
		g.dead = true
		g.mu.Unlock()
		return
	}

	g.publish(res.Emit)
	g.after()
	g.perform(res.Orders)
}

// sessionSignals tunes the pool from a session end: a clean end may grow it, a provider rate limit halves it.
func (g *group) sessionSignals(in Input) {
	if in.Kind != InSessionEnd {
		return
	}

	switch decode[SessionEndData](in.Data).Reason {
	case "done":
		if f := g.m.d.FreeMemGB; f != nil {
			g.m.pool.Grow(f())
		}
	case "provider":
		g.m.pool.Halve()
	}
}

// drainTM waits briefly for a finished group's Task Manager to end.
func (g *group) drainTM() {
	g.mu.Lock()
	done := g.tmDone
	g.mu.Unlock()
	if done == nil {
		return
	}

	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

// renew renews the lease and ends the group when another run holds it.
func (g *group) renew() {
	defer g.armRenew()

	if g.st.Stage == StageDone || g.st.Stage == NoStage {
		return
	}

	l, err := g.m.d.Leases.Renew(g.ctx, g.l)
	switch {
	case errors.Is(err, lease.ErrLeaseLost):
		g.handle(Input{Kind: InLeaseLost, Group: g.spec.ID, Time: g.now()})
	case err != nil:
		g.m.report(g, fmt.Errorf("renew the lease of %s: %w", g.spec.Label, err))
	default:
		g.l = l
	}
}

// pauseKey identifies the timers armed for a pause, so an unchanged pause keeps its timers.
type pauseKey struct {
	stage        Stage
	until, since time.Time
	cause        string
}

// newGroup returns a group that has not started.
func (m *Manager) newGroup(r *run, spec GroupSpec) *group {
	ctx, cancel := context.WithCancel(r.ctx)
	return &group{
		m: m, r: r, spec: spec, ctx: ctx, cancel: cancel, in: make(
			chan Input,
			GroupBuffer,
		), tick: make(chan struct{}, 1),
		done: make(chan struct{}), epochs: map[Stage]uint32{},
	}
}

// wakeAt stamps a wake command with the engine clock's time.
func wakeAt(w *task.Wake, now time.Time) *task.Wake {
	if w != nil {
		w.At = now
	}
	return w
}
