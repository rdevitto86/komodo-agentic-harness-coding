package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// threeTries returns limits with the shipped budget of 3 tries.
func threeTries() Limits {
	l := fixtureLimits()
	l.GroupRetries = 3
	return l
}

// failBuilder ends the claimed builder session with an idle stop under the limits.
func failBuilder(s State, l Limits) Result {
	s.Tasks[0].State, s.Tasks[0].Session = "claimed", "s1"
	return Next(s, builderEnd("idle"), l)
}

// waiting returns a group that spent its 3 tries and now waits for an override.
func waiting(t *testing.T) State {
	t.Helper()
	l := threeTries()
	s := mk(StageBuild)
	s.Counters.RetriesLeft = l.GroupRetries
	for range l.GroupRetries {
		r := failBuilder(s, l)
		if r.Refused {
			t.Fatal("a failure was refused")
		}
		s = r.State
	}
	return s
}

func TestThreeFailuresEnterTheOverrideWait(t *testing.T) {
	l := threeTries()
	s := mk(StageBuild)
	s.Counters.RetriesLeft = l.GroupRetries

	for i, want := range []int{2, 1} {
		s = failBuilder(s, l).State
		if s.Stage != StageBuild || s.Counters.RetriesLeft != want {
			t.Fatalf("failure %d: stage %q left %d, want Build and %d", i+1, s.Stage, s.Counters.RetriesLeft, want)
		}
	}

	s = failBuilder(s, l).State
	if s.Stage != StageBlocked || s.Block.Reason != state.ReasonRetries || s.Counters.RetriesLeft != 0 {
		t.Fatalf("the 3rd failure: stage %q block %+v left %d", s.Stage, s.Block, s.Counters.RetriesLeft)
	}
}

func TestTheApprovalWindowIsSixtyMinutes(t *testing.T) {
	s := waiting(t)
	if got := s.Block.Until.Sub(s.Block.At); got != 60*time.Minute {
		t.Fatalf("the window is %v", got)
	}
}

func TestAnOverrideGrantsOneFinalTryAndItsFailureFailsTheGroup(t *testing.T) {
	s := waiting(t)
	ov := in(InOverride, OverrideData{Minutes: 45})
	ov.Time = s.Block.Until.Add(-time.Minute)

	granted := Next(s, ov, threeTries())
	if granted.Refused || granted.State.Stage != StageBuild || granted.State.Counters.RetriesLeft != 1 ||
		granted.State.Counters.Overrides != 1 {
		t.Fatalf("refused %v stage %q counters %+v", granted.Refused, granted.State.Stage, granted.State.Counters)
	}

	r := failBuilder(granted.State, threeTries())
	if r.State.Stage != StageDone || r.State.Outcome != "failed" || !r.State.Block.Until.IsZero() {
		t.Fatalf("the final try's failure: stage %q outcome %q block %+v", r.State.Stage, r.State.Outcome, r.State.Block)
	}
	if slices.Contains(subtypes(r), "group.blocked") || !slices.Contains(subtypes(r), "group.failed") {
		t.Fatalf("a second wait was offered: %v", subtypes(r))
	}
}

func TestTheChosenMinutesAreStoredAndLimitTheFinalTry(t *testing.T) {
	s := waiting(t)
	ov := in(InOverride, OverrideData{Minutes: 45})
	ov.Time = s.Block.Until.Add(-time.Minute)

	r := Next(s, ov, threeTries())
	if r.State.Counters.FinalWallMin != 45 {
		t.Fatalf("stored minutes %d", r.State.Counters.FinalWallMin)
	}

	var approved bool

	for _, e := range r.Emit {
		if a, ok := e.Data.(*bus.OverrideApproved); ok {
			approved = a.Minutes == 45 && a.Granted == 1
		}
	}
	if !approved {
		t.Fatalf("override.approved did not carry the minutes: %v", subtypes(r))
	}

	// 45 minutes into the final try the extension is granted; group_wall_min of 60 alone would not have.
	f := r.State
	f.Tasks[0].State, f.Tasks[0].Session = "claimed", "s1"
	f.LastEventAt = ov.Time

	at45 := in(InFact, FactData{Subtype: "tool.denied"})
	at45.Time = ov.Time.Add(45 * time.Minute)

	ext := Next(f, at45, threeTries())
	if ext.State.Counters.WallExtTry != ext.State.Counters.Tries || ext.State.Stage != StageBuild {
		t.Fatalf("the final try's extension was not granted at 45 minutes: %+v", ext.State.Counters)
	}

	// 45 minutes plus the 60-minute extension ends the try; with none left the group fails.
	end := in(InFact, FactData{Subtype: "tool.denied"})
	end.Time = at45.Time.Add(60 * time.Minute)

	over := Next(ext.State, end, threeTries())
	if over.State.Stage != StageDone || over.State.Outcome != "failed" {
		t.Fatalf("a final try past its limit and extension: stage %q outcome %q", over.State.Stage, over.State.Outcome)
	}
}

func TestAnOutOfRangeFinalTryWindowIsRefused(t *testing.T) {
	for _, bad := range []int{-1, FinalWallMaxMin + 1} {
		s := waiting(t)
		ov := in(InOverride, OverrideData{Minutes: bad})
		ov.Time = s.Block.Until.Add(-time.Minute)

		r := Next(s, ov, threeTries())
		if !r.Refused || r.State.Counters.Overrides != 0 {
			t.Errorf("minutes %d: refused %v overrides %d", bad, r.Refused, r.State.Counters.Overrides)
		}
	}
}
