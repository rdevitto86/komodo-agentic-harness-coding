package task

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// fakeStream is a stream the test feeds by hand.
type fakeStream struct {
	ev     chan llm.StreamEvent
	errs   chan error
	onStop func(f *fakeStream, level llm.StopLevel)
	mu     sync.Mutex
	lines  []string
	stops  []llm.StopLevel
}

// newFakeStream makes a stream with room for a few queued events.
func newFakeStream() *fakeStream {
	return &fakeStream{ev: make(chan llm.StreamEvent, 16), errs: make(chan error, 4)}
}

func (f *fakeStream) Next(ctx context.Context) (llm.StreamEvent, error) {
	select {
	case e := <-f.ev:
		return e, nil
	case err := <-f.errs:
		return llm.StreamEvent{}, err
	case <-ctx.Done():
		return llm.StreamEvent{}, ctx.Err()
	}
}

func (f *fakeStream) Inject(line string) error {
	f.mu.Lock()
	f.lines = append(f.lines, line)
	f.mu.Unlock()
	return nil
}

func (f *fakeStream) Stop(level llm.StopLevel) error {
	f.mu.Lock()
	f.stops = append(f.stops, level)
	f.mu.Unlock()
	if f.onStop != nil {
		f.onStop(f, level)
	}
	return nil
}

func (f *fakeStream) Close() error { return nil }

// injected returns the lines sent so far.
func (f *fakeStream) injected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

// stopped returns the ladder rungs applied so far.
func (f *fakeStream) stopped() []llm.StopLevel {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.StopLevel(nil), f.stops...)
}

// resultEvent is the final stream event with a small output.
func resultEvent(out string) llm.StreamEvent {
	return llm.StreamEvent{Kind: llm.KindResult, Result: &llm.Result{Output: json.RawMessage(out)}}
}

// fastHooks ticks every few milliseconds and leaves the rest empty.
func fastHooks() Hooks {
	return Hooks{Every: 5 * time.Millisecond, TermAfter: 20 * time.Millisecond, KillAfter: 20 * time.Millisecond}
}

func TestSuperviseReturnsTheResultAsDone(t *testing.T) {
	s := newFakeStream()
	s.ev <- llm.StreamEvent{Kind: llm.KindOutput, Raw: json.RawMessage(`{"a":1}`)}
	s.ev <- resultEvent(`{"status":"done"}`)
	var raw []string
	h := fastHooks()
	h.Raw = func(b []byte) { raw = append(raw, string(b)) }
	res, reason, err := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: time.Hour}, &Clocks{Started: time.Now(), LastLine: time.Now()}, h)
	if err != nil || reason != state.ReasonDone || string(res.Output) != `{"status":"done"}` {
		t.Fatalf("got %s, %s, %v", res.Output, reason, err)
	}
	if len(raw) != 1 || raw[0] != `{"a":1}` {
		t.Fatalf("raw lines %v", raw)
	}
}

func TestSuperviseAsksForTimeAtTheWarningAndInjectsTheNewLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra time.Duration
		want  string
	}{{"grant", 10 * time.Minute, "Finish the current task and stop; 21 minutes remain"}, {"zero grant", 0, "Finish the current task and stop; 11 minutes remain"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStream()
			var asked []time.Duration
			h := fastHooks()
			h.Extend = func(_ context.Context, used, limit time.Duration) (time.Duration, error) {
				asked = append(asked, limit)
				return tc.extra, nil
			}
			var facts []string
			var mu sync.Mutex
			h.Fact = func(sub string, _ map[string]any) { mu.Lock(); facts = append(facts, sub); mu.Unlock() }
			clk := &Clocks{Started: time.Now().Add(-49 * time.Minute), LastLine: time.Now()}
			go func() {
				for len(s.injected()) == 0 {
					time.Sleep(2 * time.Millisecond)
				}
				s.ev <- resultEvent(`{}`)
			}()
			_, reason, err := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: 2 * time.Hour}, clk, h)
			if err != nil || reason != state.ReasonDone {
				t.Fatalf("got %s, %v", reason, err)
			}
			got := s.injected()
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("injected %q, want %q", got, tc.want)
			}
			if len(asked) != 1 || asked[0] != time.Hour {
				t.Fatalf("asked for time %v", asked)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(facts) != 1 || facts[0] != "session.timewarn" {
				t.Fatalf("facts %v", facts)
			}
		})
	}
}

func TestSuperviseNudgesASilentSessionOnceThenClimbsTheLadder(t *testing.T) {
	s := newFakeStream()
	s.onStop = func(f *fakeStream, level llm.StopLevel) {
		if level == llm.Kill {
			f.errs <- io.ErrClosedPipe
		}
	}
	now := time.Now()
	clk := &Clocks{Started: now, LastLine: now, Warned: true}
	_, reason, err := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: 10 * time.Millisecond, Grace: 20 * time.Millisecond}, clk, fastHooks())
	if err != nil || reason != state.ReasonIdle {
		t.Fatalf("got %s, %v", reason, err)
	}
	if got := s.injected(); len(got) != 1 || !strings.HasPrefix(got[0], "Report status or stop now") {
		t.Fatalf("nudges %q", got)
	}
	stops := s.stopped()
	if len(stops) != 3 || stops[0] != llm.Interrupt || stops[1] != llm.Terminate || stops[2] != llm.Kill {
		t.Fatalf("ladder %v, want interrupt, terminate, kill", stops)
	}
}

func TestSuperviseKeepsThePartialResultOfAnInterruptedTurn(t *testing.T) {
	s := newFakeStream()
	s.onStop = func(f *fakeStream, level llm.StopLevel) {
		if level == llm.Interrupt {
			f.ev <- resultEvent(`{"partial":true}`)
		}
	}
	clk := &Clocks{Started: time.Now().Add(-2 * time.Hour), LastLine: time.Now(), Warned: true}
	res, reason, err := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: time.Hour}, clk, fastHooks())
	if err != nil || reason != state.ReasonTimeout || string(res.Output) != `{"partial":true}` {
		t.Fatalf("got %s, %s, %v", res.Output, reason, err)
	}
	if got := s.stopped(); len(got) != 1 || got[0] != llm.Interrupt {
		t.Fatalf("ladder %v, want the interrupt only", got)
	}
}

func TestSuperviseCancelClimbsTheLadderAndEndsCancelled(t *testing.T) {
	s := newFakeStream()
	s.onStop = func(f *fakeStream, level llm.StopLevel) { f.errs <- io.ErrClosedPipe }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, reason, err := SuperviseWith(ctx, s, SessionLimits{Wall: time.Hour, Idle: time.Hour}, &Clocks{Started: time.Now(), LastLine: time.Now()}, fastHooks())
	if err != nil || reason != state.ReasonCancelled {
		t.Fatalf("got %s, %v", reason, err)
	}
}

func TestSuperviseMapsStreamErrorsToExitReasons(t *testing.T) {
	limit := &llm.ProviderLimit{Link: llm.ClaudeCLI, Cap: llm.CapWindow}
	for _, tc := range []struct {
		err  error
		want Reason
	}{
		{limit, state.ReasonProvider},
		{&llm.EndError{Reason: state.ReasonSchemaFail}, state.ReasonSchemaFail},
		{&llm.EndError{Reason: state.ReasonCheckFail}, state.ReasonCheckFail},
		{io.EOF, state.ReasonCrash},
	} {
		s := newFakeStream()
		s.errs <- tc.err
		_, reason, _ := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: time.Hour}, &Clocks{Started: time.Now(), LastLine: time.Now()}, fastHooks())
		if reason != tc.want {
			t.Fatalf("%v ended %s, want %s", tc.err, reason, tc.want)
		}
	}
}

func TestSuperviseRaisesOneLimitWarnOnOverage(t *testing.T) {
	s := newFakeStream()
	for range 3 {
		s.ev <- llm.StreamEvent{Kind: llm.KindRateLimit, RateLimit: &llm.RateLimit{IsUsingOverage: true}}
	}
	s.ev <- resultEvent(`{}`)
	var facts []string
	h := fastHooks()
	h.Fact = func(sub string, _ map[string]any) { facts = append(facts, sub) }
	_, reason, err := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: time.Hour}, &Clocks{Started: time.Now(), LastLine: time.Now()}, h)
	if err != nil || reason != state.ReasonDone || len(facts) != 1 || facts[0] != "limit.warn" {
		t.Fatalf("got %s, %v, facts %v", reason, err, facts)
	}
}

func TestSuperviseInjectsTheTokenCapNudge(t *testing.T) {
	s := newFakeStream()
	inj := make(chan string, 1)
	h := fastHooks()
	h.Inject = inj
	inj <- capNudgeBuilder
	go func() {
		for len(s.injected()) == 0 {
			time.Sleep(time.Millisecond)
		}
		s.ev <- resultEvent(`{}`)
	}()
	if _, _, err := SuperviseWith(context.Background(), s, SessionLimits{Wall: time.Hour, Idle: time.Hour}, &Clocks{Started: time.Now(), LastLine: time.Now()}, h); err != nil {
		t.Fatal(err)
	}
	if got := s.injected(); len(got) != 1 || got[0] != capNudgeBuilder {
		t.Fatalf("injected %q", got)
	}
}
