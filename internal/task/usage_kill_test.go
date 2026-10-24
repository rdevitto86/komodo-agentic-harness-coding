package task

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// msgEvent is an assistant line that carries one message's usage.
func msgEvent(id string, in, out int) llm.StreamEvent {
	return llm.StreamEvent{
		Kind: llm.KindOutput,
		Raw:  json.RawMessage(`{"type":"assistant"}`),
		Msg:  &llm.MsgUsage{ID: id, Usage: llm.Usage{TokensIn: in, TokensOut: out, CacheRead: 100, CacheWrite: 10}},
	}
}

// crashAfter ends the stream with err once the session has read every queued event.
func crashAfter(s *fakeStream, err error) {
	go func() {
		for len(s.ev) > 0 {
			time.Sleep(time.Millisecond)
		}

		time.Sleep(5 * time.Millisecond)
		s.errs <- err
	}()
}

// superviseOnce runs a stream to its end with generous clocks.
func superviseOnce(s *fakeStream) (llm.Result, Reason, error) {
	return SuperviseWith(
		context.Background(),
		s,
		SessionLimits{Wall: time.Hour, Idle: time.Hour},
		&Clocks{Started: time.Now(), LastLine: time.Now()},
		fastHooks(),
	)
}

func TestSuperviseKeepsTheStreamedUsageOfASessionThatCrashed(t *testing.T) {
	s := newFakeStream()
	s.ev <- msgEvent("m1", 3, 20)
	s.ev <- msgEvent("m2", 4, 30)
	crashAfter(s, io.ErrUnexpectedEOF)

	res, reason, _ := superviseOnce(s)
	if reason != state.ReasonCrash {
		t.Fatalf("reason %s, want crash", reason)
	}

	want := llm.Usage{TokensIn: 7, TokensOut: 50, CacheRead: 200, CacheWrite: 20}
	if res.Usage == nil || *res.Usage != want {
		t.Fatalf("usage %+v, want %+v", res.Usage, want)
	}
}

func TestSuperviseCountsARepeatedMessageIdOnce(t *testing.T) {
	s := newFakeStream()
	s.ev <- msgEvent("m1", 3, 20)
	s.ev <- msgEvent("m1", 3, 20)
	crashAfter(s, io.ErrUnexpectedEOF)

	res, _, _ := superviseOnce(s)
	if res.Usage == nil || res.Usage.TokensIn != 3 || res.Usage.TokensOut != 20 {
		t.Fatalf("usage %+v, want 3 in and 20 out", res.Usage)
	}
}

func TestSuperviseKeepsTheStreamedUsageWhenTheLadderEndsTheSession(t *testing.T) {
	s := newFakeStream()
	s.onStop = func(f *fakeStream, level llm.StopLevel) {
		if level == llm.Kill {
			f.errs <- io.ErrClosedPipe
		}
	}
	s.ev <- msgEvent("m1", 3, 20)

	clk := &Clocks{Started: time.Now().Add(-2 * time.Hour), LastLine: time.Now(), Warned: true}
	res, reason, _ := SuperviseWith(
		context.Background(),
		s,
		SessionLimits{Wall: time.Hour, Idle: time.Hour},
		clk,
		fastHooks(),
	)
	if reason != state.ReasonTimeout || res.Usage == nil || res.Usage.TokensOut != 20 {
		t.Fatalf("got %s with usage %+v, want timeout with 20 out", reason, res.Usage)
	}
}

func TestSuperviseLetsTheResultUsageWinOverTheStreamedUsage(t *testing.T) {
	s := newFakeStream()
	s.ev <- msgEvent("m1", 3, 20)

	done := resultEvent(`{"items":[]}`)
	done.Result.Usage = &llm.Usage{TokensIn: 11, TokensOut: 12}
	s.ev <- done

	res, reason, _ := superviseOnce(s)
	if reason != state.ReasonDone || res.Usage == nil || res.Usage.TokensIn != 11 || res.Usage.TokensOut != 12 {
		t.Fatalf("got %s with usage %+v, want done with 11 in and 12 out", reason, res.Usage)
	}
}

func TestSuperviseFillsAResultThatCarriesNoUsage(t *testing.T) {
	s := newFakeStream()
	s.ev <- msgEvent("m1", 3, 20)

	done := resultEvent(`{"items":[]}`)
	done.Result.Usage = &llm.Usage{}
	s.ev <- done

	res, _, _ := superviseOnce(s)
	if res.Usage == nil || res.Usage.TokensOut != 20 {
		t.Fatalf("usage %+v, want 20 out", res.Usage)
	}
}

func TestSessionEndCarriesTheStreamedUsageOfACrashedSession(t *testing.T) {
	r := newPipeRig(t, rigOpts{limits: Limits{GroupRetries: -1}})
	crashed := func(llm.Session) (llm.Stream, error) {
		st := newFakeStream()
		st.ev <- msgEvent("m1", 3, 20)
		st.ev <- msgEvent("m2", 4, 30)
		crashAfter(st, io.ErrUnexpectedEOF)
		return st, nil
	}
	r.model.on("builder", crashed, crashed, crashed)

	_ = r.run()

	if r.rt.factCount("usage") < 1 {
		t.Fatal("no usage fact for the crashed session")
	}

	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()

	for _, f := range r.rt.facts {
		if f.Subtype != "session.end" {
			continue
		}

		var d struct {
			Reason string `json:"reason"`
			Tokens int64  `json:"tokens"`
			Usage  *struct {
				In  int `json:"tokens_in"`
				Out int `json:"tokens_out"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Reason == "done" || d.Tokens != 57 || d.Usage == nil || d.Usage.In != 7 || d.Usage.Out != 50 {
			t.Fatalf("session.end carries %s, want a crash with 7 in and 50 out", f.Data)
		}

		return
	}

	t.Fatal("no session.end fact")
}
