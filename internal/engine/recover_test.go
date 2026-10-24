package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// savedGroup plants the record a dead engine left for a group of the rig.
func (r *rig) savedGroup(label string, stage Stage, edit func(*State)) {
	r.t.Helper()
	spec := r.plans.specs[label]
	s := NewState("old-run", spec, "hash")
	s.Stage, s.Epoch, s.Fence = stage, 2, 1
	s.Counters.RetriesLeft = 2
	s.LastEventAt = t0
	if edit != nil {
		edit(&s)
	}
	r.store.put(spec.ID, s, 5)
}

func TestRecoveryAfterKillChargesNoAttemptAndOrdersTheStageAgain(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	r.savedGroup("tg-1", StageBuild, func(s *State) {
		s.Tasks[0].State, s.Tasks[0].Session = "claimed", "s1"
	})
	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the stage ordered again", func() bool { return countCmd(r.tasks.commands("g-tg-1"), task.CmdStart) == 1 })
	until(t, "the crash session end", func() bool { return r.ev.count(bus.TypeRunStatus, "session.end") == 1 })
	s := r.store.get("g-tg-1")
	if s.Counters.RetriesLeft != 2 {
		t.Fatalf("the crash was charged: retries left %d", s.Counters.RetriesLeft)
	}
	if s.Tasks[0].State == "claimed" {
		t.Fatal("the dead session still holds its task")
	}
	if s.Unfinished == nil || s.Unfinished.Task != "t-tg-1-1" {
		t.Fatalf("unfinished edits %+v", s.Unfinished)
	}
	if r.tasks.startedCount("g-tg-1") != 1 || s.Run != "run-2" {
		t.Fatalf("started %d, run %q", r.tasks.startedCount("g-tg-1"), s.Run)
	}
}

func TestResumeLostFactIsPublished(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}})
	r.tasks.script = func(string) func(*tmEnv) {
		return func(e *tmEnv) {
			defer close(e.out)
			for {
				select {
				case c := <-e.cmds:
					if c.Kind == task.CmdStart && e.store.get(e.spec.ID).Stage == StageBuild {
						e.say(fact("session.resume-lost", map[string]any{"agent": "builder"}))
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
	r.savedGroup("tg-1", StageBuild, nil)
	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}
	until(t, "resume-lost event", func() bool { return r.ev.count(bus.TypeRunStatus, "session.resume-lost") == 1 })
}

func TestOpenOrderRunsAgainOnceByItsKeyAndIsThenClosed(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	r.savedGroup("tg-1", StageBuild, func(s *State) {
		o := Order{Kind: OrderEnterStage, Key: "g-tg-1/enter_stage/2", Stage: StageBuild}
		s.ActionsOpen = []state.OpenAction{{ID: o.Key, Kind: string(o.Kind), Args: mustJSON(o)}}
	})
	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the open order closed", func() bool { return len(r.store.get("g-tg-1").ActionsOpen) == 0 })
	time.Sleep(50 * time.Millisecond)
	if n := countCmd(r.tasks.commands("g-tg-1"), task.CmdStart); n != 1 {
		t.Fatalf("the order ran %d times", n)
	}
}

func TestGroupSavedInCommitReentersTestAndReviewBeforeCommit(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	r.savedGroup("tg-1", StageCommit, func(s *State) {
		o := Order{Kind: OrderEnterStage, Key: "g-tg-1/enter_stage/2", Stage: StageCommit}
		s.ActionsOpen = []state.OpenAction{{ID: o.Key, Kind: string(o.Kind), Args: mustJSON(o)}}
		s.Tasks[0].State = "done"
	})
	if err := r.startRun("run-2", "tg-1"); err != nil {
		t.Fatal(err)
	}
	st := r.waitHeld("tg-1", StageTestReview)
	if st.Epoch != 3 {
		t.Fatalf("epoch %d, want 3", st.Epoch)
	}
	for _, a := range st.ActionsOpen {
		if rawOrder(a).Stage == StageCommit {
			t.Fatalf("a Commit order is still open: %+v", a)
		}
	}
}

func TestPausedAndBlockedGroupsContinueFromSavedStateWithNoEventLog(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, onlyIdle)
	r.savedGroup("tg-1", StagePaused, func(s *State) {
		s.Prior, s.Pause = StageBuild, state.PauseState{Cause: "spending"}
	})
	r.savedGroup("tg-2", StageBlocked, func(s *State) {
		s.Prior, s.Block = StageBuild, state.BlockState{Reason: state.ReasonRetries}
	})
	if r.d.Reader != nil {
		t.Fatal("this test must run without an event reader")
	}
	if err := r.startRun("run-2", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-1", StageBuild)
	r.waitStage("tg-2", StageBuild)
}

func TestDoneGroupIsSkippedAndAGroupAnotherRunHoldsEndsLost(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, onlyIdle, func(r *rig) { r.leases.held["g-tg-2"] = "other-run" })
	r.savedGroup("tg-1", StageDone, func(s *State) { s.Outcome = "complete" })
	r.savedGroup("tg-2", StageBuild, nil)
	if err := r.startRun("run-2", "tg-1", "tg-2"); err != nil {
		t.Fatal(err)
	}
	r.waitStage("tg-2", StageDone)
	if got := r.store.get("g-tg-2").Outcome; got != "lost" {
		t.Fatalf("outcome %q", got)
	}
	if r.tasks.startedCount("g-tg-1") != 0 || r.tasks.startedCount("g-tg-2") != 0 {
		t.Fatal("a Task Manager started for a Done or lost group")
	}
}

// countCmd counts the commands of a kind a group's Task Managers received.
func countCmd(cs []task.Command, k task.CommandKind) int {
	n := 0
	for _, c := range cs {
		if c.Kind == k {
			n++
		}
	}
	return n
}

// blockedAttempts plants a Blocked crash group whose continue the State Manager refuses to save.
func blockedAttempts(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	r.store.monotone.Store(true)
	r.savedGroup("tg-1", StageBlocked, func(s *State) {
		s.Prior, s.Block = StageBuild, state.BlockState{Reason: state.ReasonCrash}
		s.Counters.FaultStreak = 3
	})
	return r
}

// wantRefusedContinue checks the failed save of a continue came back as a refusal, was published and left the group held.
func wantRefusedContinue(t *testing.T, r *rig, err error, refusals int) {
	t.Helper()
	var ce *codeError
	if !errors.As(err, &ce) || ce.code != CodeRunRefused || !strings.Contains(ce.detail, "lower than the stored value") {
		t.Fatalf("reply %v, want a run_refused naming the refused save", err)
	}
	until(t, "request.refused published", func() bool {
		return r.ev.count(bus.TypeRunStatus, "request.refused") == refusals
	})
	if r.ev.count(bus.TypeRunStatus, "request.accepted") != 0 {
		t.Fatal("request.accepted published for a continue that was not saved")
	}
	if st := r.store.get("g-tg-1"); st.Stage != StageBlocked || st.Block.Reason != state.ReasonCrash {
		t.Fatalf("group %s %s, want it held Blocked crash", st.Stage, st.Block.Reason)
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	if r.m.groups["g-tg-1"].dead {
		t.Fatal("a held group was marked dead by the refused continue")
	}
}

func TestRefusedSaveOfAFirstContinueRefusesTheRunStart(t *testing.T) {
	r := blockedAttempts(t)
	wantRefusedContinue(t, r, r.startRun("run-2", "tg-1"), 1)
}

func TestRefusedSaveOfASecondRunStartRefusesIt(t *testing.T) {
	r := blockedAttempts(t)
	wantRefusedContinue(t, r, r.startRun("run-2", "tg-1"), 1)
	wantRefusedContinue(t, r, r.startRun("run-3", "tg-1"), 2)
}

func TestRefusedSaveOfARunContinueRefusesIt(t *testing.T) {
	r := blockedAttempts(t)
	wantRefusedContinue(t, r, r.startRun("run-2", "tg-1"), 1)

	ch := r.m.Expect("req-cont")
	rc := &bus.RunContinue{Target: "ep-1", Groups: []string{"tg-1"}, Request: "req-cont"}
	if err := r.bus.Publish(Event{Type: bus.TypeRunContinue, Run: "run-2", Data: rc}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-ch:
		wantRefusedContinue(t, r, err, 2)
	case <-time.After(5 * time.Second):
		t.Fatal("the continue was not settled")
	}
}

func TestRunOnAFailedGroupIsRefusedAndTellsThePersonToPlanAgain(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, onlyIdle)
	r.savedGroup("tg-1", StageDone, func(s *State) { s.Outcome = "failed" })

	var ce *codeError
	if err := r.startRun("run-2", "tg-1"); !errors.As(err, &ce) || ce.code != CodeGroupFailed ||
		!strings.Contains(ce.detail, "plan it again") {
		t.Fatalf("reply %v, want a group_failed refusal", err)
	}
	if got := r.store.get("g-tg-1"); got.Stage != StageDone || got.Outcome != "failed" {
		t.Fatalf("the failed group was changed: %s %s", got.Stage, got.Outcome)
	}
}
