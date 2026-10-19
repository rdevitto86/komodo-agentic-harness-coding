package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

func TestAStateStoreFailureIsSevereStopsStartsAndOnlyAPersonResumes(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}, {"tg-2"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	r.waitStage("tg-1", StageBuild)
	r.store.failSave.Store(true)

	in := Input{Kind: InFault, Group: "g-tg-1", Data: mustJSON(FaultData{Reason: "setup", Detail: "x"})}
	if err := r.m.Report(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	until(t, "the severe alert", func() bool { return strings.Contains(r.m.errs.alert(), "SEVERE") })
	until(t, "the engine.failed event", func() bool { return r.ev.count(bus.TypeRunFailure, "engine.failed") == 1 })

	if r.m.errs.allow("g-other") {
		t.Fatal("a session start went through after an engine failure")
	}

	r.clock.Advance(24 * time.Hour)
	time.Sleep(50 * time.Millisecond)

	if !strings.Contains(r.m.errs.alert(), "SEVERE") {
		t.Fatal("the engine resumed by itself")
	}

	r.store.failSave.Store(false)
	_ = r.startRun("run-2", "tg-2")

	if got := r.m.errs.alert(); got != "" {
		t.Fatalf("a person's run left the alert %q", got)
	}
}

func TestAnUnexpectedBusClosureIsSevere(t *testing.T) {
	r := newRig(t, [][]string{{"tg-1"}}, withClock, onlyIdle)
	if err := r.startRun("run-1", "tg-1"); err != nil {
		t.Fatal(err)
	}

	r.waitStage("tg-1", StageBuild)
	_ = r.bus.Close()

	in := Input{Kind: InFault, Group: "g-tg-1", Data: mustJSON(FaultData{Reason: "setup", Detail: "x"})}
	_ = r.m.Report(context.Background(), in)

	until(t, "the severe alert", func() bool { return strings.Contains(r.m.errs.alert(), "SEVERE") })
}
