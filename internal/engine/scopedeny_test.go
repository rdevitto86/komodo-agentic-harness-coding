package engine

import (
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// A Task Manager that denied a scope request itself and resumed its builder leaves the group in Build with the ask spent.
func TestTaskManagerAutoDeniedScopeAsksKeepTheGroupInBuild(t *testing.T) {
	for _, paths := range [][]string{{"tg-2/file.go"}, {".git/config"}} {
		t.Run(paths[0], func(t *testing.T) { autoDeniedAsksKeepTheGroupInBuild(t, paths) })
	}
}

// autoDeniedAsksKeepTheGroupInBuild plays the session ends of two auto-denied asks on one task and checks the group.
func autoDeniedAsksKeepTheGroupInBuild(t *testing.T, paths []string) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}})
	r.tasks.script = func(g string) func(*tmEnv) {
		if g != "g-tg-1" {
			return idleScript
		}
		return func(e *tmEnv) {
			defer close(e.out)
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
							// The Task Manager resumes the same session after each denial, so only the first adds an attempt.
							for n, s := range []string{"s1", "s1"} {
								e.say(fact("session.start", map[string]any{
									"agent":   "builder",
									"task":    e.spec.Tasks[0],
									"session": s,
									"link":    "claude-cli",
									"model":   "m",
									"stage":   "build",
									"resumed": n > 0,
								}))
								e.say(fact("session.end", map[string]any{
									"agent":        "builder",
									"task":         e.spec.Tasks[0],
									"session":      s,
									"link":         "claude-cli",
									"reason":       "done",
									"stage":        "build",
									"scope_paths":  paths,
									"scope_reason": "needs a helper",
								}))
							}
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
	until(t, "the task's ask is spent", func() bool {
		st := r.store.get("g-tg-1")
		return len(st.Tasks) > 0 && st.Tasks[0].ScopeAsked
	})
	time.Sleep(100 * time.Millisecond)

	st := r.store.get("g-tg-1")
	if st.Stage != StageBuild {
		t.Fatalf("stage %q (%s) after auto-denied asks, want build", st.Stage, st.Block.Reason)
	}
	if n := r.ev.count(bus.TypeRunFailure, "group.blocked"); n != 0 {
		t.Fatalf("%d group.blocked events for auto-denied asks", n)
	}
	for _, c := range r.tasks.commands("g-tg-1") {
		if c.Kind == task.CmdContinue {
			t.Fatalf("the engine sent a continue %+v for an ask the Task Manager already denied", c)
		}
	}
}
