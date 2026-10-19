package engine

import (
	"sync/atomic"
	"testing"
	"time"

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
								"agent": "builder", "task": id, "session": "s-" + id, "link": "claude-cli", "model": "m", "stage": "build",
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
		t.Fatalf("block %+v, retries left %d: the fourth blocker must block with no retry spent", s.Block, s.Counters.RetriesLeft)
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
