package task

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestSessionEndFactCarriesTheSecondsTheSessionRan(t *testing.T) {
	var offset atomic.Int64
	r := newPipeRig(t, rigOpts{
		limits: Limits{Attempts: 1},
		mut: func(_ *GroupSpec, c *Collaborators) {
			c.Now = func() time.Time { return t0.Add(time.Duration(offset.Load())) }
		},
	})
	r.model.on("builder", func(llm.Session) (llm.Stream, error) {
		offset.Add(int64(64 * time.Second))
		st := newFakeStream()
		st.ev <- resultEvent(`{"items":[]}`)
		return st, nil
	})

	if err := r.run(); err != ErrHeld {
		t.Fatalf("Run returned %v", err)
	}

	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()

	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" {
			continue
		}

		var d struct {
			WallS int64 `json:"wall_s"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.WallS != 64 {
			t.Fatalf("session.end carries %s, want wall_s 64", f.Data)
		}

		return
	}

	t.Fatal("no session.end fact")
}

func TestSuperviseCancelWinsOverTheEndErrorTheStoppedProgramReports(t *testing.T) {
	s := newFakeStream()
	s.onStop = func(f *fakeStream, _ llm.StopLevel) {
		f.errs <- &llm.EndError{Reason: state.ReasonNoResult, Detail: "error_during_execution"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()

	_, reason, _ := SuperviseWith(
		ctx,
		s,
		SessionLimits{Wall: time.Hour, Idle: time.Hour},
		&Clocks{Started: time.Now(), LastLine: time.Now()},
		fastHooks(),
	)
	if reason != state.ReasonCancelled {
		t.Fatalf("got %s, want %s", reason, state.ReasonCancelled)
	}
}
