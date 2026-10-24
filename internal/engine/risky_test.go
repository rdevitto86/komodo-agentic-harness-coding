package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// riskyDone is the stage.done of Test and Review that carries the group's risk level.
func riskyDone(level string) Input {
	return in(InStageDone, map[string]any{"stage": StageTestReview, "risk": level})
}

// riskyBlocked is a group blocked after its checks, as row 23 leaves it.
func riskyBlocked() State {
	s := mk(StageBlocked)
	s.Prior = StageTestReview
	s.Block = state.BlockState{Reason: state.ReasonRiskyChange}
	return s
}

func TestRiskyChangeRows(t *testing.T) {
	t.Run("row 23 blocks a high or critical group and lets lower levels through", func(t *testing.T) {
		for _, lvl := range []string{"high", "critical"} {
			r := apply(t, mk(StageTestReview), riskyDone(lvl))
			if r.State.Stage != StageBlocked || r.State.Block.Reason != state.ReasonRiskyChange ||
				r.State.Prior != StageTestReview {
				t.Fatalf("%s: stage %q reason %q prior %q", lvl, r.State.Stage, r.State.Block.Reason, r.State.Prior)
			}
			if slices.Contains(kinds(r), OrderEnterStage) || slices.Contains(kinds(r), OrderCancel) {
				t.Errorf("%s: orders %v, want none: the checks are over and the Task Manager waits", lvl, kinds(r))
			}
			if !slices.Contains(subtypes(r), "group.blocked") {
				t.Errorf("%s: emits %v", lvl, subtypes(r))
			}
		}

		for _, lvl := range []string{"", "low", "med_high"} {
			if r := apply(t, mk(StageTestReview), riskyDone(lvl)); r.State.Stage != StageCommit {
				t.Errorf("%q: stage %q, want commit", lvl, r.State.Stage)
			}
		}
	})

	t.Run("row 24 lands an approved group", func(t *testing.T) {
		r := apply(t, riskyBlocked(), in(InContinue, ContinueData{Answer: "approve"}))
		if r.State.Stage != StageCommit || r.State.Epoch != 4 || r.State.Block.Reason != "" {
			t.Fatalf("stage %q epoch %d block %+v", r.State.Stage, r.State.Epoch, r.State.Block)
		}
		if !slices.Contains(kinds(r), OrderEnterStage) ||
			!slices.Contains(subtypes(r), "group.resumed") || !slices.Contains(subtypes(r), "stage.enter") {
			t.Errorf("orders %v emits %v", kinds(r), subtypes(r))
		}
	})

	t.Run("row 25 drops a denied group like abandon", func(t *testing.T) {
		r := apply(t, riskyBlocked(), in(InContinue, ContinueData{Answer: "deny"}))
		if r.State.Stage != StageDone || r.State.Outcome != "dropped" {
			t.Fatalf("stage %q outcome %q", r.State.Stage, r.State.Outcome)
		}
		for _, k := range []OrderKind{OrderCancel, OrderDiscard, OrderRelease} {
			if !slices.Contains(kinds(r), k) {
				t.Errorf("orders %v lack %s", kinds(r), k)
			}
		}
		if got := r.Emit[len(r.Emit)-1]; got.Subtype != "group.done" || got.Data.(*bus.GroupDone).Outcome != "abandoned" {
			t.Errorf("last event %+v, want group.done with the abandoned end status", got)
		}
	})

	t.Run("a plain continue is refused", func(t *testing.T) {
		c := in(InContinue, nil)
		c.Request = "q1"
		s := riskyBlocked()
		r := Next(s, c, fixtureLimits())
		if !r.Refused || !stateEqual(r.State, s) {
			t.Fatalf("refused=%v", r.Refused)
		}
	})
}

// riskyScript reports a clean Test and Review of risk "critical" for the label tg-1; every other group runs the happy path.
func riskyScript(group string) func(*tmEnv) {
	if group != "g-tg-1" {
		return nil
	}

	return func(e *tmEnv) {
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

				switch st := e.store.get(e.spec.ID); st.Stage {
				case StageIntake:
					e.say(fact("ready", nil))
				case StageBuild:
					e.say(append(sessionFacts("t-tg-1-1"), fact("stage.done", StageDoneData{Stage: StageBuild}))...)
				case StageTestReview:
					e.say(fact("stage.done", map[string]any{"stage": StageTestReview, "risk": "critical"}))
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
}

// riskyRig runs tg-1 (critical), tg-2 (depends on tg-1) and tg-3 (independent) and waits until tg-1 is blocked.
func riskyRig(t *testing.T) (*rig, *fakeCleaner) {
	t.Helper()

	cl := &fakeCleaner{}
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2", "tg-1"}, {"tg-3"}}, func(r *rig) {
		r.d.Cleaner = cl
		r.tasks.script = riskyScript
	})
	if err := r.startRun("run-1", "tg-1", "tg-2", "tg-3"); err != nil {
		t.Fatal(err)
	}

	st := r.waitHeld("tg-1", StageBlocked)
	if st.Block.Reason != state.ReasonRiskyChange || st.Prior != StageTestReview {
		t.Fatalf("block %+v prior %q", st.Block, st.Prior)
	}

	return r, cl
}

func TestRiskyGroupBlocksWhileAnIndependentGroupFinishes(t *testing.T) {
	r, _ := riskyRig(t)

	if st := r.waitHeld("tg-3", StageDone); st.Outcome != "complete" {
		t.Fatalf("the independent group ended %q", st.Outcome)
	}
	if n := r.tasks.startedCount("g-tg-2"); n != 0 {
		t.Fatalf("the dependent started %d times while the risky group waits", n)
	}

	_, blocked := r.m.Snapshot("")
	if len(blocked) != 1 || blocked[0].Reason != "risky_change" || blocked[0].Question != "tg-1: land this risky change?" {
		t.Fatalf("blocked rows %+v", blocked)
	}
}

func TestRiskyGroupDenyDropsItAndBlocksTheDependent(t *testing.T) {
	r, cl := riskyRig(t)

	if err := r.m.Answer("ep-1", []string{"tg-1"}, false, "d1"); err != nil {
		t.Fatal(err)
	}

	if st := r.waitHeld("tg-1", StageDone); st.Outcome != "dropped" {
		t.Fatalf("outcome %q", st.Outcome)
	}

	waitUntil(t, "discard", func() bool { cl.mu.Lock(); defer cl.mu.Unlock(); return len(cl.gone) == 1 })
	waitUntil(t, "the lease released", func() bool { return r.leases.releases("g-tg-1") == 1 })
	waitUntil(t, "tg-3 done", func() bool { return r.stageOf("tg-3") == StageDone })

	var on string

	waitUntil(t, "the dependent blocked on tg-1", func() bool {
		r.m.mu.Lock()
		defer r.m.mu.Unlock()
		on = r.m.groups["g-tg-1"].r.depBlocked["g-tg-2"]
		return on != ""
	})

	if on != "tg-1" || r.tasks.startedCount("g-tg-2") != 0 {
		t.Fatalf("dependent blocked on %q, started %d times", on, r.tasks.startedCount("g-tg-2"))
	}
}

func TestRiskyGroupBulkApproveLandsItAndNamesIt(t *testing.T) {
	r, _ := riskyRig(t)

	var ce *codeError

	if err := r.m.Answer("ep-1", []string{"tg-3"}, true, "x"); !errors.As(err, &ce) || ce.code != CodeNotBlocked {
		t.Fatalf("answer on a finished group gave %v", err)
	}

	risky, err := r.m.AnswerNaming("ep-1", nil, true, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(risky, []string{"tg-1"}) {
		t.Fatalf("named %v, want the risky group", risky)
	}

	waitUntil(t, "tg-1 landed", func() bool { return r.stageOf("tg-1") == StageDone })
	waitUntil(t, "tg-2 done", func() bool { return r.stageOf("tg-2") == StageDone })

	if st := r.store.get("g-tg-1"); st.Outcome != "complete" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	if !hasCommand(r.tasks.commands("g-tg-1"), task.CmdStart) {
		t.Fatal("the Task Manager was never started again")
	}
}
