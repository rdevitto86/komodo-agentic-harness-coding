package intake

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// readyBus closes ready once a subscription exists, so a test publishes only after Run listens.
type readyBus struct {
	bus.Bus
	ready chan struct{}
}

func (b readyBus) Subscribe(name string, types ...bus.Type) (*bus.Subscription, error) {
	s, err := b.Bus.Subscribe(name, types...)
	if name == "intake" {
		close(b.ready)
	}
	return s, err
}

func TestRunPublishesOneRunStartWithItsOwnRunID(t *testing.T) {
	r := newRig(t, twoGroupDraft())
	out, plan := runRig(t, r)
	plan(PlanRequest{Goal: "g"}, "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57")
	e := next(t, out)
	rs, ok := e.Data.(*bus.RunStart)
	if e.Type != bus.TypeRunStart || !ok {
		t.Fatalf("event: %+v", e)
	}
	if rs.Source != "intake" || rs.Target != "ep-1" || !slices.Equal(rs.Groups, []string{"tg-1.1", "tg-1.2"}) ||
		rs.Repo != "github.com/acme/shop" || rs.Root != r.root || rs.CommonDir == "" {
		t.Fatalf("data: %+v", rs)
	}
	if e.Run == "" || e.Run == "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57" || !uuid7.MatchString(string(e.Run)) {
		t.Fatalf("run id: %q", e.Run)
	}
	select {
	case extra := <-out.Events:
		t.Fatalf("second event: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunHandlesPlansOneAfterAnother(t *testing.T) {
	r := newRig(t, twoGroupDraft(), draftOf(goodGroup(2, 1, tgSpec{})))
	out, plan := runRig(t, r)
	plan(PlanRequest{Goal: "first"}, "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b01")
	plan(PlanRequest{Goal: "second"}, "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b02")
	first, second := next(t, out).Data.(*bus.RunStart), next(t, out).Data.(*bus.RunStart)
	if first.Target != "ep-1" || second.Target != "ep-2" || !slices.Equal(second.Groups, []string{"tg-2.1"}) {
		t.Fatalf("targets %q %q", first.Target, second.Target)
	}
}

func TestRunPublishesPlanBlockedAndNoRunStart(t *testing.T) {
	r := newRig(t, badDraft(), badDraft(), badDraft())
	out, plan := runRig(t, r)
	run := bus.ID("0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57")
	plan(PlanRequest{Goal: "g"}, run)
	e := next(t, out)
	pb, ok := e.Data.(*PlanBlocked)
	if e.Type != bus.TypeError || e.Subtype != "plan-blocked" || !ok || len(pb.Findings) == 0 || pb.DraftPath == "" ||
		e.Run != run {
		t.Fatalf("event: %+v", e)
	}
}

func TestRunPublishesTheErrorEachFailureMapsTo(t *testing.T) {
	cases := map[string]struct {
		req     PlanRequest
		steps   []any
		subtype bus.Subtype
	}{
		"missing document": {PlanRequest{Docs: []string{"docs/none.md"}}, nil, "plan-input"},
		"session error":    {PlanRequest{Goal: "g"}, []any{errors.New("down")}, "plan-input"},
		"deadline": {PlanRequest{Goal: "g"}, []any{func(ctx context.Context) (PlanDraft, error) {
			<-ctx.Done()
			return PlanDraft{}, ctx.Err()
		}}, "plan-timeout"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, c.steps...)
			r.h.wallUnit = 20 * time.Millisecond
			out, plan := runRig(t, r)
			plan(c.req, "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57")
			if e := next(t, out); e.Type != bus.TypeError || e.Subtype != c.subtype {
				t.Fatalf("event: %+v", e)
			}
		})
	}
}

func TestFaultDataMapsAWriteFailure(t *testing.T) {
	if pw, ok := faultData(writeError{errors.New("disk")}, 1).(*PlanWrite); !ok || pw.Reason != "disk" {
		t.Fatalf("write: %+v", pw)
	}
	if pi, ok := faultData(LockedError{Run: "r1"}, 1).(*PlanInput); !ok || pi.Reason == "" {
		t.Fatalf("locked: %+v", pi)
	}
}

func TestRegisterEventsBindsEveryPair(t *testing.T) {
	registerOnce.Do(RegisterEvents)
	for _, p := range []bus.Payload{&PlanRequest{}, &PlanBlocked{}, &PlanTimeout{}, &PlanInput{}, &PlanWrite{}} {
		b := bus.New(bus.Options{})
		k := p.EventKey()
		if err := b.Publish(bus.Event{Type: k.Type, Subtype: k.Subtype, Run: newID(), Data: p}); err != nil {
			t.Errorf("%v: %v", k, err)
		}
		_ = b.Close()
	}
}

// runRig starts Run on the rig's handler and returns a subscription to run-start and error events.
func runRig(t *testing.T, r *rig) (*bus.Subscription, func(PlanRequest, bus.ID)) {
	t.Helper()
	ready := make(chan struct{})
	r.h.d.Bus = readyBus{Bus: r.bus, ready: ready}
	out, err := r.bus.Subscribe("test", bus.TypeRunStart, bus.TypeError)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	<-ready
	return out, func(req PlanRequest, run bus.ID) {
		t.Helper()
		if err := r.bus.Publish(bus.Event{Type: bus.TypePlan, Subtype: "request", Run: run, Data: &req}); err != nil {
			t.Fatal(err)
		}
	}
}

// next returns the next event on the subscription or fails after a wait.
func next(t *testing.T, s *bus.Subscription) bus.Event {
	t.Helper()
	select {
	case e := <-s.Events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return bus.Event{}
}
