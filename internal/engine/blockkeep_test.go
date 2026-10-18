package engine

import (
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// A Task Manager that holds a scope request must still be up to take the answer after the block's cancel order.
func TestBlockedScopeGroupKeepsItsTaskManagerForTheAnswer(t *testing.T) {
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
