package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// stalledRuntime is a task.Runtime whose state report waits until it is released.
type stalledRuntime struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *stalledRuntime) Escalate(context.Context, task.Escalation) error { return nil }

func (s *stalledRuntime) ExtendTimer(context.Context, task.TimerRequest) (task.TimerGrant, error) {
	return task.TimerGrant{}, nil
}

// ReportState signals that it was entered, then waits for the release.
func (s *stalledRuntime) ReportState(ctx context.Context, _ task.StateReport) error {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return nil
}

// serveScript runs the real Task Manager command loop for a group whose pipeline never answers: the start command is
// answered with ready and then swallowed, as if one model session ran for good. Commands put on inject reach the loop.
func serveScript(t *testing.T, rt task.Runtime, inject <-chan task.Command) func(*tmEnv) {
	wt := t.TempDir()
	return func(e *tmEnv) {
		m, err := task.New(
			task.GroupSpec{Run: e.spec.Run, Group: e.spec.Label, Lease: task.LeaseRef{Worktree: wt}},
			task.Collaborators{Runtime: rt, BeatEvery: 5 * time.Millisecond},
		)
		if err != nil {
			close(e.out)
			return
		}

		fwd := make(chan task.Command, 8)

		go func() {
			for {
				select {
				case c := <-e.cmds:
					if c.Kind == task.CmdStart {
						e.say(fact("ready", nil))
						continue
					}

					fwd <- c
				case c := <-inject:
					fwd <- c
				case <-e.ctx.Done():
					return
				}
			}
		}()

		_ = m.Serve(e.ctx, task.Channels{Commands: fwd, Replies: e.out})
	}
}

// beatAt returns the time of a group's last beat.
func (r *rig) beatAt(label string) time.Time {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return r.m.beats["task:g-"+label]
}

func TestTaskManagerInALongSessionIsNeverRestarted(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) {
		r.d.Timing.LoopSilentFor = 0
		r.tasks.script = func(string) func(*tmEnv) { return serveScript(t, nil, nil) }
	})
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	r.waitStage("tg-1", StageBuild)
	until(t, "the first beat", func() bool { return !r.beatAt("tg-1").IsZero() })

	// The session stays silent for 4 x 40 s of engine time, more than twice LoopSilentFor.
	for range 4 {
		r.clock.Advance(LoopSilentFor * 2 / 3)

		want := r.clock.Now()
		until(t, "a beat after the silence", func() bool { return !r.beatAt("tg-1").Before(want) })
		r.m.CheckBeats()
	}

	if n := r.tasks.startedCount("g-tg-1"); n != 1 {
		t.Fatalf("%d Task Managers started, want 1", n)
	}
	if hasCommand(r.tasks.commands("g-tg-1"), task.CmdStop) {
		t.Fatal("the engine stopped a Task Manager that was working")
	}
}

func TestTaskManagerWhoseLoopHangsIsKilledAndRestarted(t *testing.T) {
	stall := &stalledRuntime{release: make(chan struct{}), entered: make(chan struct{})}
	inject := make(chan task.Command, 1)

	var starts atomic.Int32

	r := newRig(t, [][]string{{"tg-1"}}, withClock, func(r *rig) {
		r.d.Timing.LoopSilentFor = 0
		r.tasks.script = func(string) func(*tmEnv) {
			if starts.Add(1) == 1 {
				return serveScript(t, stall, inject)
			}
			return idleScript
		}
	})
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	r.waitStage("tg-1", StageBuild)
	until(t, "the first beat", func() bool { return !r.beatAt("tg-1").IsZero() })

	// A pause on an idle group reports state inline, so the command loop blocks inside the report and stops beating.
	inject <- task.Command{Kind: task.CmdPause, Link: "claude-cli"}
	<-stall.entered
	time.Sleep(50 * time.Millisecond)

	r.clock.Advance(LoopSilentFor + time.Second)
	r.m.CheckBeats()
	until(t, "the tree kill", func() bool { return r.killed.Load() >= 1 })
	close(stall.release)
	until(t, "a new Task Manager", func() bool { return r.tasks.startedCount("g-tg-1") == 2 })
}
