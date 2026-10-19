package engine

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

func TestTheTerminalNumberIsReadFromAProcStatLine(t *testing.T) {
	for _, tc := range []struct {
		stat string
		want int
		ok   bool
	}{
		{"42 (komodo) S 1 42 42 34816 42 4194560", 34816, true},
		{"42 (a b) c) S 1 42 42 0 -1 4194560", 0, true},
		{"42 (komodo) S 1", 0, false},
		{"garbage", 0, false},
	} {
		if n, ok := ttyNrOfStat(tc.stat); n != tc.want || ok != tc.ok {
			t.Errorf("%q: got %d %v, want %d %v", tc.stat, n, ok, tc.want, tc.ok)
		}
	}
}

func TestPsNamesATerminalOnlyWhenTheColumnHoldsOne(t *testing.T) {
	for out, want := range map[string]bool{"ttys003\n": true, "pts/1\n": true, "??\n": false, "?\n": false, "": false} {
		if got := psHasTerminal(out); got != want {
			t.Errorf("%q: %v, want %v", out, got, want)
		}
	}
}

func TestThePlatformCheckAnswersForAPeerOnAUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "kp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix socket here: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		got <- c
	}()

	cli, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	srv := <-got
	defer func() { _ = srv.Close() }()

	// The peer is this test process, so the answer depends on how the test was started; it must come without an error.
	if _, err := peerHasTerminal(srv); err != nil {
		t.Skipf("the platform check is unavailable: %v", err)
	}
}

// overrideReq builds the request run (id) --override sends.
func overrideReq(id string) Request {
	return Request{ID: id, Verb: VerbRun, Target: "tg-1", Flags: Flags{Override: true},
		Event: &Event{Type: bus.TypeRunContinue, Time: t0, Data: &bus.RunContinue{Target: "tg-1", Answer: "override", Request: bus.ID(id)}}}
}

// waitingForOverride starts an engine whose group waits for an override and serves it with the given terminal check.
func waitingForOverride(t *testing.T, check PeerChecker) *eng {
	t.Helper()
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle, withClock)
	x.savedGroup("tg-1", StageBlocked, func(s *State) {
		s.Prior, s.Counters.RetriesLeft = StageTestReview, 0
		s.Block = state.BlockState{Reason: state.ReasonRetries, At: t0, Until: t0.Add(30 * 60e9), Counter: "test_review"}
	})
	x.e.peer = check
	x.serve()
	x.ask(x.runReq("q1", "run-1", "tg-1"))
	x.waitStage("tg-1", StageBlocked)
	return x
}

func TestTheEngineRefusesAnOverrideFromAProcessWithNoTerminal(t *testing.T) {
	x := waitingForOverride(t, func(net.Conn) (bool, error) { return false, nil })

	reps := x.ask(overrideReq("o1"))
	if last := reps[len(reps)-1]; last.Status != StatusRefused || last.Code != CodeNoTerminal {
		t.Fatalf("reply %+v", last)
	}
	if st := x.store.get("g-tg-1"); st.Stage != StageBlocked || st.Counters.Overrides != 0 {
		t.Fatalf("the refused override changed the group: %s %+v", st.Stage, st.Counters)
	}
}

func TestTheEngineRefusesAnOverrideWhenThePeerCannotBeChecked(t *testing.T) {
	x := waitingForOverride(t, func(net.Conn) (bool, error) { return false, errNoPeerCheck })

	reps := x.ask(overrideReq("o1"))
	if last := reps[len(reps)-1]; last.Status != StatusRefused || last.Code != CodeNoTerminal {
		t.Fatalf("reply %+v", last)
	}
}

func TestTheEngineTakesAnOverrideFromAProcessWithATerminal(t *testing.T) {
	x := waitingForOverride(t, func(net.Conn) (bool, error) { return true, nil })

	reps := x.ask(overrideReq("o1"))
	if last := reps[len(reps)-1]; last.Status != StatusOK {
		t.Fatalf("reply %+v", last)
	}
	x.waitStage("tg-1", StageBuild)
	if st := x.store.get("g-tg-1"); st.Counters.Overrides != 1 {
		t.Fatalf("overrides %d", st.Counters.Overrides)
	}
}
