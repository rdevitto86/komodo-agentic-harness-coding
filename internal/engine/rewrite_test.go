package engine

import (
	"slices"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// taskStates lists "id:state" for each task of a state.
func taskStates(s State) []string {
	var out []string
	for _, t := range s.Tasks {
		out = append(out, t.ID+":"+t.State)
	}
	return out
}

func TestRewriteRow(t *testing.T) {
	t.Run("row 26 stops a working group, keeps its finished tasks and re-enters Build", func(t *testing.T) {
		s := mk(StageTestReview)
		s.Tasks = []state.TaskState{{ID: "t1", State: "done"}, {ID: "t2", State: "claimed", Session: "s2"}}
		s.Resume = []state.ResumeRef{{Agent: "builder"}}

		r := apply(t, s, in(InRewrite, RewriteData{Tasks: []string{"t1", "t3"}}))

		if r.State.Stage != StageBuild || r.State.Epoch != 4 || r.State.Prior != "" {
			t.Fatalf("stage %q epoch %d prior %q", r.State.Stage, r.State.Epoch, r.State.Prior)
		}
		if got := taskStates(r.State); !slices.Equal(got, []string{"t1:done", "t3:queued"}) {
			t.Fatalf("tasks %v", got)
		}
		if len(r.State.Resume) != 0 {
			t.Errorf("resume refs %v kept for the old task list", r.State.Resume)
		}
		if k := kinds(r); !slices.Equal(k, []OrderKind{OrderCancel, OrderEnterStage}) {
			t.Errorf("orders %v", k)
		}
		if r.Orders[1].Stage != StageBuild {
			t.Errorf("entered %q", r.Orders[1].Stage)
		}
		if st := subtypes(r); !slices.Contains(st, "group.resumed") || !slices.Contains(st, "stage.enter") {
			t.Errorf("emits %v", st)
		}
	})

	t.Run("a blocked or paused group is rewritten into Build and its hold is cleared", func(t *testing.T) {
		for _, s := range []State{riskyBlocked(), mk(StagePaused)} {
			s.Block = state.BlockState{Reason: s.Block.Reason}
			s.Pause.Cause = "x"
			s.Prior = StageBuild

			r := apply(t, s, in(InRewrite, RewriteData{Tasks: []string{"t1", "t9"}}))
			if r.State.Stage != StageBuild || r.State.Block.Reason != "" || r.State.Pause.Cause != "" {
				t.Errorf("%q: stage %q block %+v pause %+v", s.Stage, r.State.Stage, r.State.Block, r.State.Pause)
			}
		}
	})

	t.Run("open stage orders of the old plan are dropped", func(t *testing.T) {
		s := mk(StageCommit)
		s.ActionsOpen = []state.OpenAction{{ID: "a", Kind: string(OrderEnterStage)}, {ID: "b", Kind: string(OrderRelease)}}

		r := apply(t, s, in(InRewrite, RewriteData{Tasks: []string{"t1", "t2", "t3"}}))
		for _, a := range r.State.ActionsOpen {
			if a.ID == "a" {
				t.Fatalf("stage order kept: %+v", r.State.ActionsOpen)
			}
		}
	})

	t.Run("a finished task the rewrite leaves out is still kept", func(t *testing.T) {
		s := mk(StageBuild)
		s.Tasks[0].State = "done"

		r := apply(t, s, in(InRewrite, RewriteData{Tasks: []string{"t5"}}))
		if got := taskStates(r.State); !slices.Equal(got, []string{"t1:done", "t5:queued"}) {
			t.Fatalf("tasks %v", got)
		}
	})

	t.Run("a Done group, a group with no record and an unchanged list are refused", func(t *testing.T) {
		for name, s := range map[string]State{"done": mk(StageDone), "none": mk(NoStage)} {
			if r := Next(s, in(InRewrite, RewriteData{Tasks: []string{"t1", "t9"}}), fixtureLimits()); !r.Refused {
				t.Errorf("%s: not refused", name)
			}
		}

		c := in(InRewrite, RewriteData{Tasks: []string{"t1", "t2"}})
		c.Request = "q1"

		if r := Next(mk(StageBuild), c, fixtureLimits()); !r.Refused || r.Why == "" {
			t.Errorf("unchanged list: refused=%v why=%q", r.Refused, r.Why)
		}
	})
}

func TestReopenState(t *testing.T) {
	t.Run("a Done group reopens with its finished tasks, new tasks queued and fresh tries", func(t *testing.T) {
		d := mk(StageDone)
		d.Tasks = []state.TaskState{{ID: "t1", State: "done"}, {ID: "t2", State: "done"}}
		d.Counters.Tries, d.Counters.RetriesLeft, d.Counters.WallUsedS = 3, 0, 500
		d.Fence, d.LastFindings, d.FailureHash = 9, "old", "h"
		d.ActionsOpen = []state.OpenAction{{ID: "x", Kind: string(OrderRelease)}}

		s := ReopenState(d, []string{"t1", "t2", "t3"})
		if s.Stage != NoStage || s.Outcome != "" || s.Fence != 0 || s.Epoch != 3 || s.LastFindings != "" ||
			s.FailureHash != "" || len(s.ActionsOpen) != 0 || s.Counters.WallBaseS != 500 {
			t.Fatalf("reopened %+v", s)
		}
		if got := taskStates(s); !slices.Equal(got, []string{"t1:done", "t2:done", "t3:queued"}) {
			t.Fatalf("tasks %v", got)
		}

		r := apply(t, s, Input{Kind: InGroupStart, Group: "g1", Time: t0})
		if r.State.Stage != StageIntake || r.State.Epoch != 4 || r.State.Counters.Tries != 1 ||
			r.State.Counters.RetriesLeft != fixtureLimits().GroupRetries {
			t.Fatalf("started %q epoch %d counters %+v", r.State.Stage, r.State.Epoch, r.State.Counters)
		}
	})
}
