package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/resource"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// waitHeld waits for a group's saved stage and returns its state.
func (r *rig) waitHeld(label string, st Stage) State {
	r.t.Helper()
	r.waitStage(label, st)
	return r.store.get("g-" + label)
}

func TestPlanWindowPausesAfterTheStepAndResumesAtTheTimerWithoutACommand(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) {
		r.tasks.script = func(string) func(*tmEnv) { return liveScript }
		r.d.Chain = func(string) []string { return []string{"claude-cli"} }
	})
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	until(t, "session claimed", func() bool { return r.store.get("g-tg-1").Tasks[0].State == "claimed" })
	until := r.clock.Now().Add(2 * time.Hour)
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "window_full",
		Link:   "claude-cli",
		Groups: []string{"tg-1"},
		Until:  until,
	}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "Pause.After set", func() bool { return r.store.get("g-tg-1").Pause.After })
	if r.stageOf("tg-1") != StageBuild {
		t.Fatalf("paused before the session ended: %q", r.stageOf("tg-1"))
	}
	r.tasks.env(
		"g-tg-1",
	).say(
		fact("session.end", map[string]any{
			"agent":   "builder",
			"task":    "t-tg-1-1",
			"session": "s1",
			"link":    "claude-cli",
			"reason":  "done",
			"stage":   "build",
		}),
	)
	st := r.waitHeld("tg-1", StagePaused)
	if st.Pause.Cause != "plan_window" || !st.Pause.Until.Equal(until.Add(time.Minute)) {
		t.Fatalf("pause %+v", st.Pause)
	}
	waitUntil(t, "hold command", func() bool { return hasCommand(r.tasks.commands("g-tg-1"), task.CmdPause) })
	epoch := st.Epoch
	r.clock.Advance(2*time.Hour + 2*time.Minute)
	st = r.waitHeld("tg-1", StageBuild)
	if st.Epoch != epoch {
		t.Fatalf("epoch changed from %d to %d on resume", epoch, st.Epoch)
	}
	waitUntil(t, "wake command", func() bool {
		for _, c := range r.tasks.commands("g-tg-1") {
			if c.Kind == task.CmdContinue && c.Wake != nil && c.Wake.Reason == "timer" {
				return true
			}
		}
		return false
	})
}

func TestPlanWindowLeavesAGroupRunningWhenAnotherLinkIsUsable(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "window_full",
		Link:   "claude-cli",
		Groups: []string{"tg-1"},
		Until:  r.clock.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := r.stageOf("tg-1"); got != StageBuild {
		t.Fatalf("stage %q, want Build", got)
	}
}

func TestPlanWindowPausesAnIdleGroupAtOnce(t *testing.T) {
	r := newRig(
		t,
		[][]string{{"tg-1"}},
		withClock,
		onlyIdle,
		func(r *rig) { r.d.Chain = func(string) []string { return []string{"claude-cli"} } },
	)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	until := r.clock.Now().Add(time.Hour)
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "window_full",
		Link:   "claude-cli",
		Groups: []string{"tg-1"},
		Until:  until,
	}); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StagePaused)
	if !st.Pause.Until.Equal(until.Add(time.Minute)) {
		t.Fatalf("until %v", st.Pause.Until)
	}
	if r.m.pool.Used() != 0 {
		t.Fatalf("a paused group holds %d pool slots", r.m.pool.Used())
	}
}

func TestSpendCeilingHasNoTimerAndTheLeaseIsRenewedWhilePaused(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "spend_ceiling",
		Run:    "run-1",
		Groups: []string{"tg-1"},
	}); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StagePaused)
	if st.Pause.Cause != "spending" || !st.Pause.Until.IsZero() {
		t.Fatalf("pause %+v", st.Pause)
	}
	r.clock.Advance(10 * time.Hour)
	waitUntil(t, "lease renewed", func() bool { return r.leases.renewCount("g-tg-1") >= 1 })
	if got := r.stageOf("tg-1"); got != StagePaused {
		t.Fatalf("a spending pause ended on its own: %q", got)
	}
	ch := r.m.Expect("req-go")
	rc := &bus.RunContinue{Target: "tg-1", Request: "req-go"}
	if err := r.bus.Publish(Event{Type: bus.TypeRunContinue, Run: "run-1", Data: rc}); err != nil {
		t.Fatal(err)
	}
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
}

func TestDailySpendCeilingPausesNamedGroupsToTheNextMidnight(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.waitStage("tg-2", StageBuild)
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "spend_ceiling_day",
		Groups: []string{"tg-1"},
	}); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StagePaused)
	if !st.Pause.Until.Equal(nextMidnight(t0)) || st.Pause.Cause != "spending" {
		t.Fatalf("pause %+v", st.Pause)
	}
	if got := r.stageOf("tg-2"); got != StageBuild {
		t.Fatalf("the unnamed group is %q", got)
	}
}

func TestGroupTokenCapPausesWithNoTimerApproveRaisesTheCapDenyIsRefused(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "group_tokens",
		Groups: []string{"tg-1"},
	}); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StagePaused)
	if st.Pause.Cause != "group_tokens" || !st.Pause.Until.IsZero() {
		t.Fatalf("pause %+v", st.Pause)
	}
	var ce *codeError
	if err := r.m.Answer("tg-1", false, "d1"); !errors.As(err, &ce) || ce.code != CodeNotBlocked {
		t.Fatalf("deny gave %v", err)
	}
	if err := r.m.Answer("tg-1", true, "a1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	if len(r.res.granted) != 1 || r.res.granted[0] != "g-tg-1" {
		t.Fatalf("grants %v", r.res.granted)
	}
}

func TestPauseOrderForAnUnhostedGroupFails(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}})
	if err := r.m.PauseGroups(context.Background(), resource.PauseOrder{
		Reason: "spend_ceiling",
		Groups: []string{"nope"},
	}); err == nil {
		t.Fatal("no error for a group the engine does not host")
	}
	if err := r.m.NudgeGroups(context.Background(), resource.NudgeOrder{Groups: []string{"nope"}}); err == nil {
		t.Fatal("no error for a nudge to a group the engine does not host")
	}
}

func TestNudgeSendsOneCommandAndChangesNoState(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	waitUntil(t, "the stage orders are done", func() bool { return len(r.store.get("g-tg-1").ActionsOpen) == 0 })
	before := r.store.saves.Load()
	if err := r.m.NudgeGroups(context.Background(), resource.NudgeOrder{Groups: []string{"tg-1"}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "nudge command", func() bool { return hasCommand(r.tasks.commands("g-tg-1"), task.CmdNudge) })
	time.Sleep(50 * time.Millisecond)
	n := 0
	for _, c := range r.tasks.commands("g-tg-1") {
		if c.Kind == task.CmdNudge {
			n++
		}
	}
	if n != 1 || r.store.saves.Load() != before {
		t.Fatalf("%d nudges, %d extra saves", n, r.store.saves.Load()-before)
	}
}

func TestStartWithEveryLinkLimitedPausesThenResumesAtTheTimer(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock)
	r.tasks.script = limitedScript(r, time.Hour, 0)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StagePaused)
	if st.Pause.Cause != "provider" || st.Prior != StageBuild || st.Pause.Since.IsZero() {
		t.Fatalf("pause %+v prior %q", st.Pause, st.Prior)
	}
	r.clock.Advance(time.Hour + 2*time.Minute)
	r.waitStage("tg-1", StageBuild)
}

func TestRefusedResumeRepausesKeepsSinceAndCountsTheRefusal(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock)
	r.tasks.script = limitedScript(r, time.Hour, 1)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	first := r.waitHeld("tg-1", StagePaused)
	r.clock.Advance(time.Hour + 2*time.Minute)
	waitUntil(t, "second pause", func() bool {
		s := r.store.get("g-tg-1")
		return s.Stage == StagePaused && s.Pause.Refused == 1
	})
	second := r.store.get("g-tg-1")
	if !second.Pause.Since.Equal(first.Pause.Since) {
		t.Fatalf("Since moved from %v to %v", first.Pause.Since, second.Pause.Since)
	}
	if second.Counters.Attempts != first.Counters.Attempts {
		t.Fatalf("a refused resume spent attempts: %d to %d", first.Counters.Attempts, second.Counters.Attempts)
	}
}

func TestProviderPausePastTheMaximumBlocksPauseTimeoutAndKeepsTheWork(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock)
	r.tasks.script = limitedScript(r, 30*time.Hour, 0)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StagePaused)
	r.clock.Advance(361 * time.Minute)
	st := r.waitHeld("tg-1", StageBlocked)
	if st.Block.Reason != state.ReasonPauseTimeout {
		t.Fatalf("reason %q", st.Block.Reason)
	}
	if len(st.Tasks) == 0 {
		t.Fatal("the saved work was dropped")
	}
}

func TestSilentLoopsAreRestartedAndATaskManagerRestartAddsOneRetry(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, onlyIdle, func(r *rig) { r.d.Timing.LoopSilentFor = 0 })
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.m.Beat("fault")
	r.m.Beat("task:g-tg-1")
	r.clock.Advance(LoopSilentFor + time.Second)
	r.m.CheckBeats()
	r.rmu.Lock()
	got := append([]string(nil), r.restart...)
	r.rmu.Unlock()
	if len(got) != 1 || got[0] != "fault" {
		t.Fatalf("restarted watcher loops %v", got)
	}
	waitUntil(t, "a new Task Manager", func() bool { return r.tasks.startedCount("g-tg-1") == 2 })
	if got := r.store.get("g-tg-1").Counters.Retries; got != 1 {
		t.Fatalf("retries %d, want 1", got)
	}
}

func TestAnswerApproveOnNeedsScopeContinuesWithTheApprovedPaths(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}})
	r.tasks.script = func(string) func(*tmEnv) {
		return func(e *tmEnv) {
			defer close(e.out)
			for {
				select {
				case c := <-e.cmds:
					// a blocked group's Task Manager stays up to take the answer
					if c.Kind != task.CmdStart {
						continue
					}
					switch e.store.get(e.spec.ID).Stage {
					case StageIntake:
						e.say(fact("ready", nil))
					case StageBuild:
						e.send(task.Message{
							ID:   "esc",
							Kind: task.MsgEscalate,
							Escalation: &task.Escalation{
								ID:     "esc",
								Group:  e.spec.ID,
								Kind:   task.EscScope,
								Reason: "needs_scope",
								Detail: mustJSON(ScopeAsk{
									Task:   e.spec.Tasks[0],
									Paths:  []string{"shared/util.go"},
									Reason: "helper",
								})}})
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StageBlocked)
	if st.Block.Reason != state.ReasonNeedsScope {
		t.Fatalf("reason %q", st.Block.Reason)
	}
	rows, blocked := r.m.Snapshot("")
	if len(rows) != 1 || len(blocked) != 1 || blocked[0].Reason != "needs_scope" {
		t.Fatalf("snapshot %+v %+v", rows, blocked)
	}
	if err := r.m.Answer("tg-1", true, "ans1"); err != nil {
		t.Fatal(err)
	}
	st = r.waitHeld("tg-1", StageBuild)
	if len(st.ScopeAdded) != 1 || st.ScopeAdded[0] != "shared/util.go" {
		t.Fatalf("ScopeAdded %v", st.ScopeAdded)
	}
	waitUntil(t, "approve answer", func() bool {
		for _, c := range r.tasks.commands("g-tg-1") {
			if c.Kind == task.CmdContinue && c.Answer != nil && c.Answer.Action == "approve" {
				return true
			}
		}
		return false
	})
	var ce *codeError
	if err := r.m.Answer("tg-1", true, "ans2"); !errors.As(err, &ce) || ce.code != CodeNotBlocked {
		t.Fatalf("answer on a working group gave %v", err)
	}
}

func TestAbandonEndsTheGroupDiscardsItAndBlocksTheDependent(t *testing.T) {
	cl := &fakeCleaner{}
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2", "tg-1"}}, onlyIdle, func(r *rig) { r.d.Cleaner = cl })
	if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	if _, err := r.m.Abandon(context.Background(), "tg-1"); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StageDone)
	if st.Outcome != "abandoned" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	waitUntil(t, "discard", func() bool { cl.mu.Lock(); defer cl.mu.Unlock(); return len(cl.gone) == 1 })
	if n := r.leases.releases("g-tg-1"); n != 1 {
		t.Fatalf("released %d times", n)
	}
	waitUntil(t, "stalled with the dependent", func() bool { return r.ev.count(bus.TypeRunFailure, "stalled") == 1 })
}

// liveScript reports ready, then holds a live builder session in Build until it is stopped.
func liveScript(e *tmEnv) {
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
			switch e.store.get(e.spec.ID).Stage {
			case StageIntake:
				e.say(fact("ready", nil))
			case StageBuild:
				e.say(
					fact("session.start", map[string]any{
						"agent":   "builder",
						"task":    e.spec.Tasks[0],
						"session": "s1",
						"link":    "claude-cli",
						"model":   "m",
						"stage":   "build",
					}),
				)
			}
		case <-e.ctx.Done():
			return
		}
	}
}

// limitedScript ends every Build session as paused at a provider limit, refusing a resume refuse times.
func limitedScript(r *rig, resetIn time.Duration, refuse int) func(string) func(*tmEnv) {
	return func(string) func(*tmEnv) {
		return func(e *tmEnv) {
			defer close(e.out)
			refused := 0
			pausedEnd := func() {
				e.say(fact(
					"session.end",
					map[string]any{
						"agent":    "builder",
						"task":     e.spec.Tasks[0],
						"session":  "s1",
						"link":     "claude-cli",
						"reason":   "paused",
						"stage":    "build",
						"reset_at": r.clock.Now().Add(resetIn)}))
			}
			for {
				select {
				case c := <-e.cmds:
					switch {
					case c.Kind == task.CmdStop:
						return
					case c.Kind == task.CmdStart && e.store.get(e.spec.ID).Stage == StageIntake:
						e.say(fact("ready", nil))
					case c.Kind == task.CmdStart && e.store.get(e.spec.ID).Stage == StageBuild:
						e.say(
							fact("session.start", map[string]any{
								"agent":   "builder",
								"task":    e.spec.Tasks[0],
								"session": "s1",
								"link":    "claude-cli",
								"model":   "m",
								"stage":   "build",
							}),
						)
						pausedEnd()
					case c.Kind == task.CmdContinue && c.Wake != nil:
						if refused < refuse {
							refused++
							pausedEnd()
						}
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
}

// waitUntil is until under a name that does not clash with a local variable.
func waitUntil(t *testing.T, what string, cond func() bool) { t.Helper(); until(t, what, cond) }

// hasCommand reports whether a command of the kind was received.
func hasCommand(cs []task.Command, k task.CommandKind) bool {
	for _, c := range cs {
		if c.Kind == k {
			return true
		}
	}
	return false
}
