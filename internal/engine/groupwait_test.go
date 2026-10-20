package engine

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// crashScript is a Task Manager whose builder session ends crash each time Build starts; builds counts those starts.
func crashScript(builds *atomic.Int64) func(string) func(*tmEnv) {
	return func(string) func(*tmEnv) {
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

					st := e.store.get(e.spec.ID)
					switch st.Stage {
					case StageIntake:
						e.say(fact("ready", nil))
					case StageBuild:
						builds.Add(1)
						id := st.Tasks[0].ID
						e.say(
							fact("session.start", map[string]any{
								"agent": "builder", "task": id, "session": "s-" + id, "link": "claude-cli",
								"model": "m", "stage": "build",
							}),
							fact("session.end", map[string]any{
								"agent": "builder", "task": id, "session": "s-" + id, "link": "claude-cli", "reason": "crash",
								"stage": "build",
							}),
						)
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
}

// streakOf returns a group's saved fault streak.
func (r *rig) streakOf(label string) int { return r.store.get("g-" + label).Counters.FaultStreak }

func TestAGroupWaitsOneTwoThenFourMinutesAfterBlockerErrorsAndTheFourthBlocksIt(t *testing.T) {
	var builds atomic.Int64

	r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) { r.tasks.script = crashScript(&builds) })
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	for n, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		until(t, "the crash of a build", func() bool { return builds.Load() == int64(n+1) && r.streakOf("tg-1") == n+1 })

		r.clock.Advance(wait - time.Second)
		time.Sleep(60 * time.Millisecond)

		if got := builds.Load(); got != int64(n+1) {
			t.Fatalf("crash %d: Build restarted %d times before its wait of %s ended", n+1, got, wait)
		}

		r.clock.Advance(time.Second)
	}

	until(t, "the fourth crash", func() bool { return builds.Load() == 4 })
	r.waitStage("tg-1", StageBlocked)

	s := r.store.get("g-tg-1")
	if s.Block.Reason != state.ReasonCrash || s.Counters.RetriesLeft != 2 {
		t.Fatalf("block %+v, retries left %d: the 4th blocker spends no retry", s.Block, s.Counters.RetriesLeft)
	}

	r.clock.Advance(10 * time.Minute)
	time.Sleep(60 * time.Millisecond)

	if got := builds.Load(); got != 4 {
		t.Fatalf("a blocked group started a fifth build: %d", got)
	}
}

func TestAWorkOutcomeResetsTheGroupsWaits(t *testing.T) {
	s := claimed()
	s.Counters.FaultStreak = 2

	r := apply(t, s, builderEnd("check_fail"))
	if r.State.Counters.FaultStreak != 0 {
		t.Fatalf("a work outcome left streak %d", r.State.Counters.FaultStreak)
	}

	if got := groupWait(r.State.Counters.FaultStreak + 1); got != time.Minute {
		t.Fatalf("the next blocker waits %s, want 1m", got)
	}
}

// judgeEndScript is a Task Manager that builds cleanly; its first Test and Review start ends the tester crash and then the
// reviewer with reason, and later ones only count; starts counts the Test and Review starts.
func judgeEndScript(starts *atomic.Int64, reason string) func(string) func(*tmEnv) {
	return func(string) func(*tmEnv) {
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

					st := e.store.get(e.spec.ID)
					switch st.Stage {
					case StageIntake:
						e.say(fact("ready", nil))
					case StageBuild:
						var fs []task.Fact
						for _, t := range st.Tasks {
							fs = append(fs, sessionFacts(t.ID)...)
						}
						e.say(append(fs, fact("stage.done", StageDoneData{Stage: StageBuild}))...)
					case StageTestReview:
						if starts.Add(1) != 1 {
							continue
						}

						e.say(fact("session.end", map[string]any{
							"agent": "tester", "session": "s-tester", "link": "claude-cli", "reason": "crash",
							"stage": "test_review",
						}))
						e.say(fact("session.end", map[string]any{
							"agent": "reviewer", "session": "s-reviewer", "link": "claude-cli", "reason": reason,
							"stage": "test_review",
						}))
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
}

func TestANonBlockerErrorLeavesTheGroupsRunningWaitArmed(t *testing.T) {
	for _, reason := range []string{"provider", "timeout", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			var starts atomic.Int64

			script := judgeEndScript(&starts, reason)

			r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) { r.tasks.script = script })
			if err := r.startRun("run-1", "tg-1"); err != nil {
				t.Fatal(err)
			}

			until(t, "the first Test and Review start", func() bool { return starts.Load() >= 1 })
			time.Sleep(150 * time.Millisecond)

			if got := starts.Load(); got != 1 {
				t.Fatalf("%d Test and Review starts before the tester's wait ended, want 1", got)
			}

			r.clock.Advance(time.Minute)
			until(t, "the held start after its wait", func() bool { return starts.Load() > 1 })
		})
	}
}

func TestASetupBlockerWaitIsNoStallAndTheGroupRetriesAfterIt(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock)
	r.tasks.failFor["g-tg-1"] = errors.New("worktree: disk full")

	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	until(t, "the setup error", func() bool { return r.ev.count(bus.TypeRunStatus, "error.recorded") == 1 })
	time.Sleep(100 * time.Millisecond)

	if n := r.ev.count(bus.TypeRunFailure, "stalled"); n != 0 {
		t.Fatalf("%d stalled events while the group waits its minute", n)
	}
	if n := r.ev.count(bus.TypeRunStatus, "group.stop"); n != 0 {
		t.Fatalf("%d group.stop events while the group waits its minute", n)
	}

	r.tasks.mu.Lock()
	delete(r.tasks.failFor, "g-tg-1")
	r.tasks.mu.Unlock()

	r.clock.Advance(time.Minute)
	until(t, "the retry after the wait", func() bool { return r.tasks.startedCount("g-tg-1") >= 1 })
}

func TestASetupBlockerWaitsOneTwoThenFourMinutesAndTheFourthIsAStall(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock)
	r.tasks.failFor["g-tg-1"] = errors.New("worktree: disk full")

	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	for n, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		until(t, "the setup error", func() bool {
			return r.ev.count(bus.TypeRunStatus, "error.recorded") == n+1 && r.streakOf("tg-1") == n+1
		})

		r.clock.Advance(wait - time.Second)
		time.Sleep(60 * time.Millisecond)

		if got := r.ev.count(bus.TypeRunStatus, "error.recorded"); got != n+1 {
			t.Fatalf("setup error %d: the group retried before its wait of %s ended", n+1, wait)
		}
		if got := r.ev.count(bus.TypeRunFailure, "stalled"); got != 0 {
			t.Fatalf("setup error %d: %d stalled events during the wait", n+1, got)
		}

		r.clock.Advance(time.Second)
	}

	until(t, "the stall of the fourth", func() bool { return r.ev.count(bus.TypeRunFailure, "stalled") == 1 })

	if got := r.ev.count(bus.TypeRunStatus, "error.recorded"); got != 4 {
		t.Fatalf("%d setup errors, want 4", got)
	}

	s := r.store.get("g-tg-1")
	if s.Stage != StageBlocked || s.Block.Reason != state.ReasonSetup || s.Counters.RetriesLeft != 2 {
		t.Fatalf("stage %q block %+v retries left %d: the 4th blocks for a person with no retry spent",
			s.Stage, s.Block, s.Counters.RetriesLeft)
	}

	r.clock.Advance(10 * time.Minute)
	time.Sleep(60 * time.Millisecond)

	if got := r.ev.count(bus.TypeRunStatus, "error.recorded"); got != 4 {
		t.Fatalf("a group blocked for a person retried: %d setup errors", got)
	}
}
