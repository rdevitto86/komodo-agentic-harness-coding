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
	want := []string{"fetch origin/main", "create komodo/ep-1 from refs/remotes/origin/main", "lists ep-1"}
	if len(got) < 4 || !slices.Equal(got[:3], want) || got[3] != "start g-tg-1.1" {
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

// The epic branch is cut from the fetched tip of the configured base on the configured remote.
func TestEpicBranchIsCutFromTheFetchedRemoteBase(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}}, func(r *rig) {
		r.d.BaseBranch, r.d.Remote = constant("komodo-first-run"), constant("trial")
	})
	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.1", StageDone)
	want := []string{
		"fetch trial/komodo-first-run",
		"create komodo/ep-1 from refs/remotes/trial/komodo-first-run",
		"lists ep-1",
	}
	if got := r.log.get(); len(got) < 3 || !slices.Equal(got[:3], want) {
		t.Fatalf("order %v, want it to begin %v", got, want)
	}
}

// A base and remote changed after the engine started are the ones the next run's epic branch is cut from.
func TestEpicBranchUsesTheBaseSetAfterTheEngineStarted(t *testing.T) {
	base, remote := "old-base", "old-remote"
	r := newRig(t, [][]string{{"tg-1.1"}}, func(r *rig) {
		r.d.BaseBranch = func() string { return base }
		r.d.Remote = func() string { return remote }
	})
	base, remote = "new-base", "new-remote"
	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.1", StageDone)
	want := []string{"fetch new-remote/new-base", "create komodo/ep-1 from refs/remotes/new-remote/new-base"}
	if got := r.log.get(); len(got) < 2 || !slices.Equal(got[:2], want) {
		t.Fatalf("order %v, want it to begin %v", got, want)
	}
}

// An empty base is the remote's default branch, and an empty remote is the base branch's upstream.
func TestEmptyBaseTakesTheRemoteDefaultBranch(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}}, func(r *rig) {
		r.d.BaseBranch = constant("")
		r.git.def, r.git.upstream = "develop", "upstream"
	})
	if err := r.startRun("run-1", "tg-1.1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1.1", StageDone)
	want := []string{
		"default origin",
		"fetch upstream/develop",
		"create komodo/ep-1 from refs/remotes/upstream/develop",
		"lists ep-1",
	}
	if got := r.log.get(); len(got) < 4 || !slices.Equal(got[:4], want) {
		t.Fatalf("order %v, want it to begin %v", got, want)
	}
}

// A base that cannot be fetched refuses the run before any branch is cut.
func TestRunIsRefusedWhenTheBaseCannotBeFetched(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1.1"}}, func(r *rig) { r.git.fetchErr = errors.New("no such remote") })
	err := r.startRun("run-1", "tg-1.1")
	if err == nil || !strings.Contains(err.Error(), "no such remote") {
		t.Fatalf("run start returned %v, want a refusal that names the fetch failure", err)
	}
	for _, l := range r.log.get() {
		if strings.HasPrefix(l, "create") || strings.HasPrefix(l, "lists") {
			t.Fatalf("a branch was cut after a failed fetch: %v", r.log.get())
		}
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
			"limits.group_retries":  2,
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
						Escalation: &task.Escalation{ID: "e1", Group: e.spec.ID, Kind: task.EscBlocked, Reason: "context"},
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
			if len(s.Groups) != 2 || s.Groups[0].Reason != "context" && s.Groups[1].Reason != "context" {
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

// A result.fail is reported after the Task Manager has moved its progress to Build, yet it belongs to Test and Review.
func TestResultFailIsStampedWithTheTestReviewEpochAndReturnsTheGroup(t *testing.T) {
	epochs := map[Stage]uint32{StageBuild: 2, StageTestReview: 3}
	m := task.Message{
		ID:   "r",
		Kind: task.MsgState,
		State: &task.StateReport{
			Progress: task.Progress{Stage: StageBuild},
			Facts: []task.Fact{
				{Subtype: "result.fail", Data: mustJSON(ResultFail{Items: []string{"TestAdd"}, Regression: true})},
			},
		},
	}
	ins := inputsFromMessage("g1", m, func(st Stage) uint32 { return epochs[st] }, t0)
	if len(ins) != 1 || ins[0].Kind != InResultFail || ins[0].Epoch != 3 {
		t.Fatalf("inputs %+v", ins)
	}

	r := Next(mk(StageTestReview), ins[0], fixtureLimits())
	if r.Stale || r.State.Stage != StageBuild || r.State.Counters.RetriesLeft != 1 {
		t.Fatalf("stale %v stage %q retries left %d", r.Stale, r.State.Stage, r.State.Counters.RetriesLeft)
	}
}

// The judges' session ends go out while the stage is Test and Review; the rework then reports the result.fail from Build.
func TestReworkRoundCountsTheJudgesTokensAndEmitsTheirSessionEnds(t *testing.T) {
	epochs := map[Stage]uint32{StageBuild: 2, StageTestReview: 3}
	end := func(agent string) task.Fact {
		return task.Fact{Subtype: "session.end", Data: []byte(`{"session":"` + agent + `1","agent":"` + agent +
			`","reason":"done","tokens":5000}`)}
	}
	msgs := []task.Message{
		{ID: "j", Kind: task.MsgState, State: &task.StateReport{
			Progress: task.Progress{Stage: StageTestReview},
			Facts:    []task.Fact{end("tester"), end("reviewer")},
		}},
		{ID: "r", Kind: task.MsgState, State: &task.StateReport{
			Progress: task.Progress{Stage: StageBuild},
			Facts:    []task.Fact{{Subtype: "result.fail", Data: mustJSON(ResultFail{Items: []string{"x"}})}},
		}},
	}

	s, ends := mk(StageTestReview), 0
	s.Counters.RetriesLeft = 3 // two silent ends and the return each cost one
	for _, m := range msgs {
		for _, in := range inputsFromMessage("g1", m, func(st Stage) uint32 { return epochs[st] }, t0) {
			r := Next(s, in, fixtureLimits())
			if r.Stale {
				t.Fatalf("%s at epoch %d is stale", in.Kind, in.Epoch)
			}
			for _, e := range r.Emit {
				if e.Subtype == "session.end" {
					ends++
				}
			}
			s = r.State
		}
	}
	if s.Counters.TokensUsed != 10000 || ends != 2 || s.Stage != StageBuild {
		t.Fatalf("tokens %d, session.end events %d, stage %s", s.Counters.TokensUsed, ends, s.Stage)
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
