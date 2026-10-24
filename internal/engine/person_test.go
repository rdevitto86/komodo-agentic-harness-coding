package engine

import (
	"errors"
	"net"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// denyReq builds the request run (id) --deny sends for a group.
func denyReq(id, target string) Request {
	data := &bus.RunContinue{Target: "ep-1", Groups: []string{target}, Answer: "deny", Request: bus.ID(id)}

	return Request{ID: id, Verb: VerbRun, Target: target, Flags: Flags{Deny: true},
		Event: &Event{Type: bus.TypeRunContinue, Time: t0, Data: data}}
}

// severeStates are the two states only a person ends, each with how a test puts the engine in it.
var severeStates = map[string]func(h *errorHandler){
	"engine failure": func(h *errorHandler) { h.engineFailed("test", errors.New("boom")) },
	"stuck pause": func(h *errorHandler) {
		h.mu.Lock()
		h.paused, h.stuck, h.reason = true, true, state.ReasonSetup
		h.mu.Unlock()
	},
}

func TestOnlyAnAcceptedRunFromATerminalEndsASevereStateOrAStuckPause(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		req      func(x *eng) Request
		cleared  bool
	}{
		{"an orchestrator deny", false, func(*eng) Request { return denyReq("r1", "tg-1") }, false},
		{"a terminal deny nothing waits for", true, func(*eng) Request { return denyReq("r1", "tg-1") }, false},
		{"an unknown group from a terminal", true, func(x *eng) Request { return x.runReq("r1", "run-1", "tg-9") }, false},
		{"a run with no terminal", false, func(x *eng) Request { return x.runReq("r1", "run-1", "tg-1") }, false},
		{"a run from a terminal", true, func(x *eng) Request { return x.runReq("r1", "run-1", "tg-1") }, true},
	}

	for kind, put := range severeStates {
		for _, c := range cases {
			t.Run(kind+"/"+c.name, func(t *testing.T) {
				x := newEng(t, [][]string{{"tg-1"}}, onlyIdle, withClock)
				x.e.peer = func(net.Conn) (bool, error) { return c.terminal, nil }
				x.serve()
				put(x.m.errs)

				if x.m.errs.alert() == "" {
					t.Fatal("the state was not put in force")
				}

				x.ask(c.req(x))

				if cleared := x.m.errs.alert() == ""; cleared != c.cleared {
					t.Fatalf("cleared %v, want %v; alert %q", cleared, c.cleared, x.m.errs.alert())
				}
			})
		}
	}
}
