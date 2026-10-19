package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

type holdLeases struct {
	*fakeLeases
	hmu  sync.Mutex
	runs map[string]string
}

var _ = sync.Mutex{}

var _ = context.Background

var _ = lease.ErrLeased

func (h *holdLeases) Grant(ctx context.Context, req lease.GrantRequest) (lease.Lease, error) {
	h.hmu.Lock()
	if r, ok := h.runs[req.Group]; ok && r != req.Run {
		h.hmu.Unlock()
		return lease.Lease{}, lease.HeldError{Holder: lease.Lease{Run: r, Group: req.Group}}
	}
	h.runs[req.Group] = req.Run
	h.hmu.Unlock()
	return h.fakeLeases.Grant(ctx, req)
}

func (h *holdLeases) Release(ctx context.Context, x lease.Lease) error {
	h.hmu.Lock()
	delete(h.runs, x.Group)
	h.hmu.Unlock()
	return h.fakeLeases.Release(ctx, x)
}

func TestStopReturnsEveryActiveGroupsPoolSlot(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	t.Logf("before stop: used=%d", r.m.pool.Used())
	if err := r.m.Stop(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	t.Logf("after stop: used=%d size=%d", r.m.pool.Used(), r.m.pool.Size())
	if r.m.pool.Used() != 0 {
		t.Fatalf("pool slot leaked after Stop: used=%d", r.m.pool.Used())
	}
}

func TestRunLosingTheLeaseNeverReplacesTheLiveGroup(t *testing.T) {
	var hl *holdLeases
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}, {"tg-3"}}, onlyIdle, func(r *rig) {
		r.d.Config = rigConfig{
			"limits.group_retries":  2,
			"limits.pause_max_min":  360,
			"limits.group_wall_min": 60,
			"limits.group_tokens":   0, "limits.pool_min": 1, "limits.pool_max": 2, "sandbox.max_mem_gb": 2}
		hl = &holdLeases{fakeLeases: r.leases, runs: map[string]string{}}
		r.d.Leases = hl
	})
	if err := r.startRun("run-A", "tg-1", "tg-2", "tg-3"); err != nil {
		t.Fatal(err)
	}
	until(t, "two running", func() bool { return r.m.pool.Used() == 2 })
	if err := r.startRun("run-B", "tg-3"); err != nil {
		t.Fatal(err)
	}
	until(t, "two TMs", func() bool {
		n := 0
		for _, l := range []string{"tg-1", "tg-2", "tg-3"} {
			n += r.tasks.startedCount("g-" + l)
		}
		return n == 2
	})
	var first []string
	for _, l := range []string{"tg-1", "tg-2", "tg-3"} {
		if r.tasks.startedCount("g-"+l) > 0 {
			first = append(first, l)
		}
	}
	t.Logf("running first: %v", first)
	for _, l := range first {
		if _, err := r.m.Abandon(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	hl.hmu.Lock()
	holder := hl.runs["g-tg-3"]
	hl.hmu.Unlock()
	r.m.mu.Lock()
	g := r.m.groups["g-tg-3"]
	r.m.mu.Unlock()
	if g == nil {
		t.Fatalf("no group registered; holder=%s", holder)
	}
	g.mu.Lock()
	dead := g.dead
	g.mu.Unlock()
	t.Logf("lease holder run=%s; m.groups[g-tg-3] belongs to run=%s dead=%v", holder, g.r.id, dead)
	if g.r.id != holder {
		t.Fatalf("m.groups routes g-tg-3 to run %s (dead=%v) while run %s holds the lease", g.r.id, dead, holder)
	}
}

func TestAbandonAndLandingPlanReadsDoNotRace(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, onlyIdle)
	if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-2", StageBuild)
	r.m.mu.Lock()
	g2 := r.m.groups["g-tg-2"]
	r.m.mu.Unlock()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				o := Order{Kind: OrderEnterStage, Stage: StageCommit}
				g2.attachLanding(&o) // what the tg-2 loop does on its Commit order
			}
		}
	}()
	if _, err := r.m.Abandon(context.Background(), "tg-1"); err != nil {
		t.Fatal(err)
	}
	close(stop)
}

func TestConcurrentRunStartsOpenOneCachePerRepository(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, onlyIdle, func(r *rig) {
		r.git.exists["komodo/ep-1"] = true
	})
	var wg sync.WaitGroup
	for i, l := range []string{"tg-1", "tg-2"} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.startRun("run-"+string(rune('A'+i)), l) }()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	r.m.mu.Lock()
	refs := -1
	if rc := r.m.repos["github.com/acme/shop"]; rc != nil {
		refs = rc.refs
	}
	r.m.mu.Unlock()
	t.Logf("opens=%d refs on the kept cache=%d", r.caches.opens.Load(), refs)
	if r.caches.opens.Load() != 1 {
		t.Fatalf("cache opened %d times for one repository", r.caches.opens.Load())
	}
}

func TestStopWithTwoStubbornGroupsFinishesWithinTheDeadline(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, func(r *rig) {
		r.tasks.script = func(string) func(*tmEnv) {
			return func(e *tmEnv) {
				defer close(e.out)
				for {
					select {
					case c := <-e.cmds:
						if c.Kind == task.CmdStart && e.store.get(e.spec.ID).Stage == StageIntake {
							e.say(fact("ready", nil))
						}
					case <-e.ctx.Done():
						return
					}
				}
			}
		}
	})
	if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.waitStage("tg-2", StageBuild)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.m.Stop(context.Background(), "run-1") }()
	select {
	case <-done:
		t.Logf("stop returned after %v", time.Since(start))
	case <-time.After(8 * time.Second):
		t.Fatalf("Stop still blocked after 4s (deadline %v)", r.d.Timing.ShutdownDeadline)
	}
}

func TestGroupThatNeverStartedIsReportedLostAndALaterRunCanStartIt(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle, func(r *rig) { r.leases.held["g-tg-1"] = "other-run" })
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	until(t, "stalled", func() bool { return r.ev.count(bus.TypeRunFailure, "stalled") == 1 })
	for _, e := range r.ev.list() {
		if s, ok := e.Data.(*Stalled); ok {
			if len(s.Groups) != 1 || s.Groups[0].Reason != "lease_lost" {
				t.Fatalf("stalled payload %+v", s.Groups)
			}
		}
	}
	r.m.mu.Lock()
	still := r.m.groups["g-tg-1"] != nil
	r.m.mu.Unlock()
	if still {
		t.Fatal("a group that never started is still registered")
	}
	r.leases.mu.Lock()
	delete(r.leases.held, "g-tg-1")
	r.leases.mu.Unlock()
	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}
	until(t, "the later run starts the group", func() bool { return r.tasks.startedCount("g-tg-1") == 1 })
}

func TestQuietTaskManagerIsRestartedOnlyAfterTheOldOneEnded(t *testing.T) {
	var oldClosed, early atomic.Bool
	r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) {
		r.d.Timing.LoopSilentFor = 0
		r.tasks.onStart = func(_ string, n int) {
			if n == 2 && !oldClosed.Load() {
				early.Store(true)
			}
		}
		r.tasks.script = func(string) func(*tmEnv) {
			return func(e *tmEnv) {
				defer close(e.out)
				first := e.store.get(e.spec.ID).Stage == StageIntake
				for c := range e.cmds {
					if c.Kind == task.CmdStart && first {
						first = false
						e.say(fact("ready", nil))
					}
					if c.Kind == task.CmdStop {
						time.Sleep(200 * time.Millisecond)
						oldClosed.Store(true)
						return
					}
				}
			}
		}
	})
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.m.Beat("task:g-tg-1")
	r.clock.Advance(LoopSilentFor + time.Second)
	r.m.CheckBeats()
	until(t, "a new Task Manager", func() bool { return r.tasks.startedCount("g-tg-1") == 2 })
	if early.Load() {
		t.Fatal("the new Task Manager started before the old one ended")
	}
}

func TestQuietTaskManagerThatIgnoresStopIsKilledThenRestarted(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) {
		r.d.Timing.LoopSilentFor = 0
		r.tasks.script = func(string) func(*tmEnv) {
			return func(e *tmEnv) {
				defer close(e.out)
				first := e.store.get(e.spec.ID).Stage == StageIntake
				for {
					select {
					case c := <-e.cmds:
						if c.Kind == task.CmdStart && first {
							first = false
							e.say(fact("ready", nil))
						}
					case <-e.ctx.Done():
						return
					}
				}
			}
		}
	})
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.m.Beat("task:g-tg-1")
	r.clock.Advance(LoopSilentFor + time.Second)
	r.m.CheckBeats()
	until(t, "the tree kill", func() bool { return r.killed.Load() >= 1 })
	until(t, "a new Task Manager", func() bool { return r.tasks.startedCount("g-tg-1") == 2 })
}

func TestRequestLineOverTheLimitGetsBadFrameAndTheConnectionStaysOpen(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	c, err := Dial(x.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	big := []byte(`{"id":"big1","verb":"status","target":"` + strings.Repeat("x", maxFrame+10) + `"}`)
	if err := c.SendRaw(big); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Recv()
	if err != nil || rep.Code != CodeBadFrame || !rep.Final {
		t.Fatalf("reply %+v err %v", rep, err)
	}
	reps := roundTrip(t, c, Request{ID: "s1", Verb: VerbStatus, Root: x.root})
	if reps[len(reps)-1].Status != StatusOK {
		t.Fatalf("status after the long line: %+v", reps)
	}
}

func TestLeaseIsReleasedWhenARecoveredGroupsFirstSaveFails(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	r.savedGroup("tg-1", StageBuild, nil)
	r.store.failSave.Store(true)
	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}
	until(t, "the lease released", func() bool { return r.leases.releases("g-tg-1") == 1 })
}

func TestLeaseIsReleasedWhenTheGrantStepIsRefused(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle, func(r *rig) { r.leases.zeroFence = true })
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	until(t, "the lease released", func() bool { return r.leases.releases("g-tg-1") == 1 })
}

func TestBeatEntryOfAFinishedGroupIsDropped(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}})
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageDone)
	until(t, "run complete", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })
	until(t, "beat entry dropped", func() bool {
		r.m.mu.Lock()
		defer r.m.mu.Unlock()
		_, ok := r.m.beats["task:g-tg-1"]
		return !ok
	})
}
