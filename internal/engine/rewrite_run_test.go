package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// mergedCommits is a Commit Handler that answers whether the epic's pull request merged.
type mergedCommits struct {
	*fakeCommits
	merged bool
	err    error
	asked  atomic.Int32
}

// EpicMerged answers with the planted value.
func (c *mergedCommits) EpicMerged(context.Context, commit.EpicPush) (bool, error) {
	c.asked.Add(1)
	return c.merged, c.err
}

// setTasks changes the task list the plan holds for a group label.
func (r *rig) setTasks(label string, tasks ...string) {
	r.plans.mu.Lock()
	defer r.plans.mu.Unlock()

	s := r.plans.specs[label]
	s.Tasks = tasks
	r.plans.specs[label] = s
}

// holdFirst is a script whose first Task Manager finishes task "a" in Build and then waits; later ones run the happy path.
func holdFirst(n *atomic.Int32) func(string) func(*tmEnv) {
	return func(string) func(*tmEnv) {
		if k := n.Add(1); k > 1 {
			// A later Task Manager numbers its messages apart from the first, as a real one does.
			return func(e *tmEnv) {
				e.seq = int(k) * 1000
				happy(e)
			}
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

					switch e.store.get(e.spec.ID).Stage {
					case StageIntake:
						e.say(fact("ready", nil))
					case StageBuild:
						e.say(sessionFacts("a")[:2]...)
					}
				case <-e.ctx.Done():
					return
				}
			}
		}
	}
}

func TestRewriteRun(t *testing.T) {
	t.Run("a running group keeps its finished tasks and re-enters Build with the rewritten tasks", func(t *testing.T) {
		var starts atomic.Int32

		r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) { r.tasks.script = holdFirst(&starts) })
		r.setTasks("tg-1", "a", "b")

		if err := r.startRun("run-1", "tg-1"); err != nil {
			t.Fatal(err)
		}

		waitUntil(t, "task a done", func() bool {
			s := r.store.get("g-tg-1")
			return s.Stage == StageBuild && len(s.Tasks) == 2 && s.Tasks[0].State == "done"
		})

		epoch := r.store.get("g-tg-1").Epoch

		r.setTasks("tg-1", "a", "c")

		if err := r.startRun("run-2", "tg-1"); err != nil {
			t.Fatal(err)
		}

		st := r.waitHeld("tg-1", StageDone)
		if st.Outcome != "complete" || st.Epoch <= epoch {
			t.Fatalf("outcome %q epoch %d (was %d)", st.Outcome, st.Epoch, epoch)
		}
		if got := taskStates(st); !slices.Equal(got, []string{"a:done", "c:done"}) {
			t.Fatalf("tasks %v", got)
		}
		if n := r.tasks.startedCount("g-tg-1"); n != 2 {
			t.Fatalf("Task Managers started %d times, want a fresh one after the rewrite", n)
		}

		var stopped bool

		for _, c := range r.tasks.commands("g-tg-1") {
			stopped = stopped || (c.Kind == task.CmdStop && c.Cause == "rewrite")
		}

		if !stopped {
			t.Fatalf("commands %+v lack the rewrite stop", r.tasks.commands("g-tg-1"))
		}
	})

	t.Run("a done group gets the new tasks, runs again and its epic is pushed again", func(t *testing.T) {
		var n atomic.Int32

		r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) { r.tasks.script = numbered(&n) })

		if err := r.startRun("run-1", "tg-1"); err != nil {
			t.Fatal(err)
		}

		r.waitStage("tg-1", StageDone)
		waitUntil(t, "the first push", func() bool { return len(r.commits.pushes()) == 1 })
		waitUntil(t, "the first run ended", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })

		r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

		if err := r.startRun("run-2", "tg-1"); err != nil {
			t.Fatal(err)
		}

		waitUntil(t, "the second push", func() bool { return len(r.commits.pushes()) == 2 })

		st := r.waitHeld("tg-1", StageDone)
		waitUntil(t, "the group done again", func() bool {
			st = r.store.get("g-tg-1")
			return st.Stage == StageDone && len(st.Tasks) == 2 && st.Tasks[1].State == "done"
		})

		if st.Outcome != "complete" || st.Run != "run-2" {
			t.Fatalf("outcome %q run %q", st.Outcome, st.Run)
		}
		if got := taskStates(st); !slices.Equal(got, []string{"t-tg-1-1:done", "t-tg-1-2:done"}) {
			t.Fatalf("tasks %v", got)
		}
		if n := r.leases.releases("g-tg-1"); n != 2 {
			t.Fatalf("lease released %d times, want 2", n)
		}
	})

	t.Run("a done group is reopened while another group keeps its run alive", func(t *testing.T) {
		var starts, ones atomic.Int32

		r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, func(r *rig) {
			r.tasks.script = func(g string) func(*tmEnv) {
				if g == "g-tg-2" {
					return holdFirst(&starts)(g)
				}

				return numbered(&ones)(g)
			}
		})

		if err := r.startRun("run-1", "tg-1", "tg-2"); err != nil {
			t.Fatal(err)
		}

		r.waitStage("tg-1", StageDone)
		r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

		if err := r.startRun("run-2", "tg-1"); err != nil {
			t.Fatal(err)
		}

		waitUntil(t, "tg-1 done with both tasks", func() bool {
			s := r.store.get("g-tg-1")
			return s.Stage == StageDone && len(s.Tasks) == 2 && s.Tasks[1].State == "done" && s.Run == "run-2"
		})
	})

	t.Run("a rewrite of a group in a merged plan is refused and the group stays done", func(t *testing.T) {
		mc := &mergedCommits{merged: true}

		r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) {
			mc.fakeCommits = r.commits
			r.d.Commits = mc
		})

		if err := r.startRun("run-1", "tg-1"); err != nil {
			t.Fatal(err)
		}

		r.waitStage("tg-1", StageDone)
		waitUntil(t, "the first run ended", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })
		r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

		err := r.startRun("run-2", "tg-1")

		var ce *codeError
		if !errors.As(err, &ce) || ce.code != CodeRunRefused || !strings.Contains(ce.detail, "write a new plan") {
			t.Fatalf("err %v", err)
		}
		if st := r.store.get("g-tg-1"); st.Stage != StageDone || len(st.Tasks) != 1 {
			t.Fatalf("group %q with %d tasks", st.Stage, len(st.Tasks))
		}
		if n := r.tasks.startedCount("g-tg-1"); n != 1 {
			t.Fatalf("Task Managers started %d times", n)
		}
	})

	t.Run("a rewrite is refused when the Commit Handler cannot tell whether the plan merged", func(t *testing.T) {
		mc := &mergedCommits{err: errors.New("gh is down")}

		r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) {
			mc.fakeCommits = r.commits
			r.d.Commits = mc
		})

		if err := r.startRun("run-1", "tg-1"); err != nil {
			t.Fatal(err)
		}

		r.waitStage("tg-1", StageDone)
		waitUntil(t, "the first run ended", func() bool { return r.ev.count(bus.TypeRunComplete, "done") == 1 })
		r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

		if err := r.startRun("run-2", "tg-1"); err == nil || !strings.Contains(err.Error(), "gh is down") {
			t.Fatalf("err %v", err)
		}
		if mc.asked.Load() != 1 {
			t.Fatalf("asked %d times", mc.asked.Load())
		}
	})

	t.Run("a run-start that changes no task continues the group as before", func(t *testing.T) {
		var starts atomic.Int32

		r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) { r.tasks.script = holdFirst(&starts) })

		if err := r.startRun("run-1", "tg-1"); err != nil {
			t.Fatal(err)
		}

		r.waitStage("tg-1", StageBuild)

		if err := r.startRun("run-2", "tg-1"); err != nil {
			t.Fatal(err)
		}
		if n := r.tasks.startedCount("g-tg-1"); n != 1 {
			t.Fatalf("Task Managers started %d times", n)
		}
	})
}

func TestRewriteSavedGroup(t *testing.T) {
	t.Run("a saved blocked group is rewritten after a restart", func(t *testing.T) {
		r := newRig(t, [][]string{{"tg-1"}})

		s := mk(StageBlocked)
		s.Run, s.Group, s.Prior = "run-0", "g-tg-1", StageBuild
		s.Tasks = []state.TaskState{{ID: "t-tg-1-1", State: "done"}}
		s.Block = state.BlockState{Reason: state.ReasonWall}
		r.store.put("g-tg-1", s, 1)
		r.setTasks("tg-1", "t-tg-1-1", "t-tg-1-2")

		if err := r.startRun("run-2", "tg-1"); err != nil {
			t.Fatal(err)
		}

		waitUntil(t, "the saved group done", func() bool {
			st := r.store.get("g-tg-1")
			return st.Stage == StageDone && len(st.Tasks) == 2 && st.Tasks[1].State == "done"
		})
	})
}

// numbered runs the happy path with each Task Manager numbering its messages apart, as real ones do.
func numbered(n *atomic.Int32) func(string) func(*tmEnv) {
	return func(string) func(*tmEnv) {
		k := n.Add(1)

		return func(e *tmEnv) {
			e.seq = int(k) * 1000
			happy(e)
		}
	}
}
