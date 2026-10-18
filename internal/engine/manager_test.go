package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

func TestFixtureGroupPassesEveryStageAndEndsDone(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}})
	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.1", StageDone)
	until(t, "run-complete done", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })
	st := r.store.get("g-tg-1.1")
	if st.Outcome != "complete" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	if n := r.leases.releases("g-tg-1.1"); n != 1 {
		t.Fatalf("the lease was released %d times", n)
	}
	if r.m.pool.Used() != 0 {
		t.Fatalf("pool still holds %d slots", r.m.pool.Used())
	}
	for _, want := range []string{"stage.enter", "group.done"} {
		if r.ev.count(bus.TypeRunStatus, want) == 0 {
			t.Fatalf("no %s event", want)
		}
	}
	if asked := r.plans.asked; len(asked) != 1 || !slices.Equal(asked[0], []string{"tg-1.1"}) {
		t.Fatalf("Specs asked for %v", asked)
	}
}

func TestEpicBranchAndTaskListsComeBeforeTheFirstTaskManager(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}, {"tg-1.2", "tg-1.1"}})
	r.tasks.script = func(g string) func(*tmEnv) {
		return func(e *tmEnv) {
			r.log.add("start " + g)
			happy(e)
		}
	}
	if err := r.startRun("run-1", "tg-1.1", "tg-1.2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.2", StageDone)
	got := r.log.get()
	want := []string{"create komodo/ep-1 from main", "lists ep-1"}
	if len(got) < 3 || !slices.Equal(got[:2], want) || got[2] != "start g-tg-1.1" {
		t.Fatalf("order %v", got)
	}
	count := 0
	for _, l := range got {
		if strings.HasPrefix(l, "create ") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the epic branch was created %d times", count)
	}
}

func TestExistingEpicBranchIsLeftAsItIs(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}}, func(r *rig) { r.git.exists["komodo/ep-1"] = true })
	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.1", StageDone)
	if got := r.log.get(); len(got) != 0 {
		for _, l := range got {
			if strings.HasPrefix(l, "create") || strings.HasPrefix(l, "lists") {
				t.Fatalf("existing branch was touched: %v", got)
			}
		}
	}
}

func TestCacheOpensOnceAndClosesWithTheLastRun(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}, {"tg-2.1"}})
	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	if err := r.startRun("run-2", "tg-2.1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.1", StageDone)
	r.waitStage("tg-2.1", StageDone)
	until(t, "both runs complete", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 2 })
	if r.caches.opens.Load() != 1 {
		t.Fatalf("the cache opened %d times", r.caches.opens.Load())
	}
	until(t, "cache closed", func() bool { return r.caches.closes.Load() == 1 })
	if len(r.ctxs.used) != 1 {
		t.Fatalf("the Context Manager got %d handles", len(r.ctxs.used))
	}
}

func TestPoolLimitsActiveGroupsAndDependentsWait(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}, {"tg-3"}, {"tg-4", "tg-1"}}, onlyIdle, func(r *rig) {
		r.d.Config = rigConfig{
			"limits.returns":        2,
			"limits.group_attempts": 2,
			"limits.pause_max_min":  360,
			"limits.group_wall_min": 60,
			"limits.group_tokens":   0, "limits.pool_min": 1, "limits.pool_max": 2, "sandbox.max_mem_gb": 2}
	})
	if err := r.startRun("run-1", "tg-1", "tg-2", "tg-3", "tg-4"); err != nil {
		t.Fatal(err)
	}
	until(t, "two groups running", func() bool { return r.m.pool.Used() == 2 })
	time.Sleep(100 * time.Millisecond)
	started := 0
	for _, l := range []string{"tg-1", "tg-2", "tg-3", "tg-4"} {
		started += r.tasks.startedCount("g-" + l)
	}
	if started != 2 || r.m.pool.Used() != 2 {
		t.Fatalf("%d Task Managers started, pool used %d", started, r.m.pool.Used())
	}
	if r.tasks.startedCount("g-tg-4") != 0 {
		t.Fatal("a group started before its dependency was Done")
	}
}

func TestBlockedDependencyKeepsDependentWaitingWhileUnrelatedGroupRuns(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2", "tg-1"}, {"tg-3"}})
	r.tasks.script = func(g string) func(*tmEnv) {
		if g != "g-tg-1" {
			return nil
		}
		return func(e *tmEnv) {
			defer close(e.out)
			for c := range e.cmds {
				if c.Kind == task.CmdStart {
					e.send(task.Message{
						Kind:       task.MsgEscalate,
						Escalation: &task.Escalation{ID: "e1", Group: e.spec.ID, Kind: task.EscBlocked, Reason: "setup"},
					})
				}
			}
		}
	}
	if err := r.startRun("run-1", "tg-1", "tg-2", "tg-3"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-3", StageDone)
	r.waitStage("tg-1", StageBlocked)
	if r.tasks.startedCount("g-tg-2") != 0 {
		t.Fatal("the dependent started")
	}
	until(t, "stalled event", func() bool { return r.ev.count(bus.TypeRunFailure, "stalled") == 1 })
	for _, e := range r.ev.list() {
		if s, ok := e.Data.(*Stalled); ok {
			if len(s.Groups) != 2 || s.Groups[0].Reason != "setup" && s.Groups[1].Reason != "setup" {
				t.Fatalf("stalled data %+v", s.Groups)
			}
		}
	}
}

func TestPanicInOneGroupBlocksOnlyThatGroup(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, func(r *rig) { r.ctxs.panics["g-tg-1"] = true })
	if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-2", StageDone)
	r.waitStage("tg-1", StageBlocked)
	if got := r.store.get("g-tg-1").Block.Reason; got != state.ReasonCrash {
		t.Fatalf("block reason %q", got)
	}
}

func TestScopeRequestOnAnotherGroupsFileIsDeniedWithoutAsking(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}})
	r.tasks.script = func(g string) func(*tmEnv) {
		if g != "g-tg-1" {
			return idleScript
		}
		return func(e *tmEnv) {
			defer close(e.out)
			asked := 0
			for {
				select {
				case c := <-e.cmds:
					switch c.Kind {
					case task.CmdStop:
						return
					case task.CmdStart:
						switch e.store.get(e.spec.ID).Stage {
						case StageIntake:
							e.say(fact("ready", nil))
						case StageBuild:
							asked++
							e.send(task.Message{
								ID:   "esc",
								Kind: task.MsgEscalate,
								Escalation: &task.Escalation{
									ID:     "esc",
									Group:  e.spec.ID,
									Kind:   task.EscScope,
									Reason: "needs_scope",
									Detail: mustJSON(ScopeAsk{
										Task:   "t-tg-1-1",
										Paths:  []string{"tg-2/file.go"},
										Reason: "shared helper",
									})}})
						}
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
	if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	until(t, "a deny answer", func() bool {
		for _, c := range r.tasks.commands("g-tg-1") {
			if c.Kind == task.CmdContinue && c.Answer != nil && c.Answer.Action == "deny" {
				return true
			}
		}
		return false
	})
	st := r.store.get("g-tg-1")
	if st.Stage != StageBuild {
		t.Fatalf("stage %q after a denial", st.Stage)
	}
}

func TestRenewRefusedEndsTheGroupLostAndReleasesNothing(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.leases.mu.Lock()
	r.leases.renewErr["g-tg-1"] = lease.ErrLeaseLost
	r.leases.mu.Unlock()
	until(t, "renew timer armed", func() bool { return r.clock.Pending() > 0 })
	r.clock.Advance(LeaseRenewEvery + time.Second)
	r.waitStage("tg-1", StageDone)
	if got := r.store.get("g-tg-1").Outcome; got != "lost" {
		t.Fatalf("outcome %q", got)
	}
	if n := r.leases.releases("g-tg-1"); n != 0 {
		t.Fatalf("a lost lease was released %d times", n)
	}
}

func TestLeaseHeldByAnotherRunNeverStartsTheGroup(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) { r.leases.held["g-tg-1"] = "other-run" })
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	until(t, "lease.lost event", func() bool { return r.ev.count(bus.TypeRunFailure, "lease.lost") == 1 })
	if r.tasks.startedCount("g-tg-1") != 0 {
		t.Fatal("a Task Manager started without the lease")
	}
}

func TestRunRefusedForACycleAndForUnknownLabel(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1", "tg-2"}, {"tg-2", "tg-1"}})
	err := r.startRun("run-1", "tg-1", "tg-2")
	var ce *codeError
	if !errors.As(err, &ce) || ce.code != CodeRunRefused {
		t.Fatalf("cycle gave %v", err)
	}
	until(t, "run-refused event", func() bool { return r.ev.count(bus.TypeError, "run-refused") == 1 })
	err = r.startRun("run-2", "nope")
	if !errors.As(err, &ce) {
		t.Fatalf("unknown label gave %v", err)
	}
}

func TestDependencyOutsideTheRunMustBeDone(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2", "tg-1"}})
	err := r.startRun("run-1", "tg-2")
	var ce *codeError
	if !errors.As(err, &ce) || ce.code != CodeRunRefused || !strings.Contains(ce.detail, "tg-1") {
		t.Fatalf("got %v", err)
	}
}

func TestStopKillsAtTheDeadlineSavesAndRevokesBeforeRunComplete(t *testing.T) {
	r2 := newRig(t, [][]string{{"tg-1"}}, func(r *rig) {
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
	if err := r2.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r2.waitStage("tg-1", StageBuild)
	start := time.Now()
	if err := r2.m.Stop(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("stop took %v", time.Since(start))
	}
	if r2.killed.Load() == 0 {
		t.Fatal("the whole-tree kill never ran")
	}
	until(t, "run-complete stopped", func() bool { return r2.ev.count(bus.TypeRunComplete, "stopped") == 1 })
	if got := r2.log.get(); !slices.Contains(got, "revoke g-tg-1") {
		t.Fatalf("lease not revoked: %v", got)
	}
	if r2.stageOf("tg-1") != StageBuild {
		t.Fatalf("a stopped group lost its stage: %q", r2.stageOf("tg-1"))
	}
}

func TestReportForAnUnknownGroupFails(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}})
	if err := r.m.Report(context.Background(), Input{Kind: InReady, Group: "nope"}); err == nil {
		t.Fatal("Report accepted an input for no group")
	}
}

func TestReaderGoroutineMapsEachMessageToItsInput(t *testing.T) {
	var got []InputKind
	epoch := func(Stage) uint32 { return 4 }
	now := t0
	msgs := []task.Message{
		{ID: "a", Kind: task.MsgTimer, Timer: &task.TimerRequest{Session: "s"}},
		{ID: "b", Kind: task.MsgEnded, Ended: &task.Ended{Result: "closed"}},
		{ID: "c", Kind: task.MsgEnded, Ended: &task.Ended{Result: "failed", Error: "boom"}},
		{ID: "d", Kind: task.MsgEnded, Ended: &task.Ended{Result: "held"}},
		{
			ID:         "e",
			Kind:       task.MsgEscalate,
			Escalation: &task.Escalation{ID: "x", Kind: task.EscScope, Detail: mustJSON(ScopeAsk{Task: "t"})},
		},
		{ID: "f", Kind: task.MsgEscalate, Escalation: &task.Escalation{ID: "y", Kind: task.EscFailed, Reason: "setup"}},
		{
			ID:   "g",
			Kind: task.MsgState,
			State: &task.StateReport{
				Progress: task.Progress{Stage: StageBuild},
				Facts:    []task.Fact{{Subtype: "ready"}, {Subtype: "stage.done"}, {Subtype: "tool.denied"}},
			},
		},
	}
	for _, m := range msgs {
		for _, in := range inputsFromMessage("g1", m, epoch, now) {
			got = append(got, in.Kind)
		}
	}
	want := []InputKind{InTimerExtend, InGroupClosed, InFault, InScopeAsk, InFault, InReady, InStageDone, InFact}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEachCommandKindReachesTheTaskManagerAndItsMessageComesBack(t *testing.T) {
	cases := []struct {
		order Order
		want  task.CommandKind
	}{
		{Order{Kind: OrderStartGroup}, task.CmdStart},
		{Order{Kind: OrderHold}, task.CmdPause},
		{Order{Kind: OrderCancel}, task.CmdStop},
		{Order{Kind: OrderExtend, Data: mustJSON(map[string]any{"extra": 1})}, task.CmdContinue},
		{Order{Kind: OrderScope, Data: mustJSON(ScopeAnswer{Answer: "approve"})}, task.CmdContinue},
	}
	for _, c := range cases {
		cmd, ok := commandFor(c.order, State{})
		if !ok || cmd.Kind != c.want {
			t.Fatalf("%s gave %v ok=%v", c.order.Kind, cmd.Kind, ok)
		}
	}
	if _, ok := commandFor(Order{Kind: OrderRelease}, State{}); ok {
		t.Fatal("release became a command")
	}
}

// withClock runs the rig on its fake clock.
func withClock(r *rig) {
	r.d.Clock = r.clock
	r.d.Timing.LoopSilentFor = 10000 * time.Hour // a test that jumps the clock is not a silent loop
}

// idleScript is a Task Manager that reports ready, then does nothing until it is stopped.
func idleScript(e *tmEnv) {
	defer close(e.out)
	first := true
	for {
		select {
		case c := <-e.cmds:
			if c.Kind == task.CmdStop {
				return
			}
			if c.Kind == task.CmdStart && first && e.store.get(e.spec.ID).Stage == StageIntake {
				first = false
				e.say(fact("ready", nil))
			}
		case <-e.ctx.Done():
			return
		}
	}
}

// onlyIdle makes every group's Task Manager idle after ready.
func onlyIdle(r *rig) { r.tasks.script = func(string) func(*tmEnv) { return idleScript } }
