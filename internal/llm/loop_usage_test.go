package llm

import (
	"errors"
	"testing"
)

func TestLoopStreamsTheUsageOfEachTurnSoAKilledSessionKeepsIt(t *testing.T) {
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){
		callTool("t1", "read", `{}`),
		callTool("t2", "read", `{}`),
		func(turnRequest) (turnResult, error) { return turnResult{}, errors.New("the connection dropped") },
	}}
	cfg := loopSession(t, w, &fakeHost{}, nil, 100000)
	cfg.Price = func(u Usage, _ int) float64 { return float64(u.TokensOut) / 10 }

	evs, err := run(t, cfg)
	if err == nil {
		t.Fatal("the session ended with a result")
	}

	var tl UsageTally
	for _, ev := range evs {
		tl.Add(ev.Msg)
	}

	want := Usage{TokensIn: 20, TokensOut: 10, USD: 1}
	if got := tl.Total(); got == nil || *got != want {
		t.Fatalf("streamed usage %+v, want %+v", got, want)
	}
}

func TestAnEmptyTurnStillStreamsItsUsage(t *testing.T) {
	w := &fakeWire{script: []func(turnRequest) (turnResult, error){
		callTool("t1", "read", `{}`),
		func(turnRequest) (turnResult, error) {
			return turnResult{Usage: Usage{TokensIn: 7, TokensOut: 3}}, nil
		},
	}}
	cfg := loopSession(t, w, &fakeHost{}, nil, 100000)

	evs, err := run(t, cfg)
	if err == nil {
		t.Fatal("the session ended with a result")
	}

	var tl UsageTally
	for _, ev := range evs {
		tl.Add(ev.Msg)
	}

	want := Usage{TokensIn: 17, TokensOut: 8}
	if got := tl.Total(); got == nil || *got != want {
		t.Fatalf("streamed usage %+v, want %+v", got, want)
	}
}
