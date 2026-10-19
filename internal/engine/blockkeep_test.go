package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// A Task Manager that holds a scope request must still be up to take the answer after the block's cancel order.
func TestBlockedScopeGroupKeepsItsTaskManagerForTheAnswer(t *testing.T) {
	for _, endFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("session_end_first_%v", endFirst), func(t *testing.T) { blockedScopeKeepsItsTaskManager(t, endFirst) })
	}
}

// blockedScopeKeepsItsTaskManager runs the scenario with the builder's needs_scope session.end before the ask or without it.
func blockedScopeKeepsItsTaskManager(t *testing.T, endFirst bool) {
	r := newRig(t, [][]string{{"tg-1"}})
	answered := make(chan task.Command, 4)
	r.tasks.script = func(string) func(*tmEnv) {
		return func(e *tmEnv) {
			defer close(e.out)
			for {
				select {
				case c := <-e.cmds:
					switch {
					case c.Kind == task.CmdStop:
						return
					case c.Kind == task.CmdContinue && c.Answer != nil:
						answered <- c
					case c.Kind == task.CmdStart:
						switch e.store.get(e.spec.ID).Stage {
						case StageIntake:
							e.say(fact("ready", nil))
						case StageBuild:
							if endFirst {
								e.say(fact("session.end", map[string]any{
									"session":      "s1",
									"agent":        "builder",
									"task":         e.spec.Tasks[0],
									"reason":       "needs_scope",
									"scope_paths":  []string{"shared/util.go"},
									"scope_reason": "helper",
								}))
							}
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
	r.waitHeld("tg-1", StageBlocked)
	if err := r.m.Answer("tg-1", true, "ans1"); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-answered:
		if c.Answer.Action != "approve" || c.Answer.Escalation != "esc" {
			t.Fatalf("answer %+v", c.Answer)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the scope answer never reached a Task Manager: the block's stop ended the one that held the request")
	}
}

// A fact with no registered event reaches the error reporter, which writes the engine log.
func TestDroppedFactIsWrittenToTheErrorOutput(t *testing.T) {
	var mu sync.Mutex
	var errs []string
	r := newRig(t, [][]string{{"tg-1"}}, func(r *rig) {
		r.d.Errors = func(_, _ string, err error) {
			mu.Lock()
			errs = append(errs, err.Error())
			mu.Unlock()
		}
	})
	r.tasks.script = func(string) func(*tmEnv) {
		return func(e *tmEnv) {
			defer close(e.out)
			for {
				select {
				case c := <-e.cmds:
					if c.Kind == task.CmdStart && e.store.get(e.spec.ID).Stage == StageIntake {
						e.say(fact("ready", nil), fact("bogus.fact", map[string]string{"a": "b"}))
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
	until(t, "the dropped fact in the error output", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(errs) == 1 && strings.Contains(errs[0], "bogus.fact")
	})
}
