package cli

import (
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// overrideMinutes runs run --override with a terminal and returns the minutes the engine was sent, or -1 for no request.
func overrideMinutes(t *testing.T, stdin string, args ...string) (int, int, string, string) {
	t.Helper()
	h := newHarness(t)
	h.terminal, h.stdin = true, stdin
	repo := repoDir(t, "")
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(map[string]any{})) })

	code, _, errOut := h.run(append([]string{"--json", "-C", repo, "run", "tg-1.2", "--override"}, args...)...)
	reqs := srv.Requests()
	if len(reqs) == 0 {
		return code, -1, errOut, ""
	}

	rc, ok := reqs[0].Event.Data.(*bus.RunContinue)
	if !ok || !reqs[0].Flags.Override || rc.Answer != "override" {
		t.Fatalf("request = %+v", reqs[0])
	}

	return code, rc.Minutes, errOut, rc.Target
}

func TestOverrideMinutesFlagSkipsThePromptAndTravelsInTheRequest(t *testing.T) {
	code, minutes, errOut, _ := overrideMinutes(t, "", "--minutes", "45")
	if code != 0 || minutes != 45 || strings.Contains(errOut, "Working time") {
		t.Fatalf("exit %d minutes %d stderr %q", code, minutes, errOut)
	}
}

func TestOverridePromptsForTheWorkingTimeAndEnterAcceptsTheDefault(t *testing.T) {
	code, minutes, errOut, _ := overrideMinutes(t, "\n")
	if code != 0 || minutes != 60 || !strings.Contains(errOut, "Working time for the final try, in minutes [60]: ") {
		t.Fatalf("exit %d minutes %d stderr %q", code, minutes, errOut)
	}

	if code, minutes, _, _ := overrideMinutes(t, "90\n"); code != 0 || minutes != 90 {
		t.Fatalf("typed 90: exit %d minutes %d", code, minutes)
	}
}

func TestOverrideMinutesOutOfRangeSendNothing(t *testing.T) {
	for _, tc := range []struct{ stdin, flag string }{{"", "0"}, {"", "481"}, {"", "-3"}, {"999\n", ""}, {"abc\n", ""}} {
		args := []string{}
		if tc.flag != "" {
			args = []string{"--minutes", tc.flag}
		}
		if code, minutes, _, _ := overrideMinutes(t, tc.stdin, args...); code == 0 || minutes != -1 {
			t.Errorf("%+v: exit %d minutes %d", tc, code, minutes)
		}
	}
}

func TestOverrideMinutesNeedsOverrideAndATerminal(t *testing.T) {
	h := newHarness(t)
	if _, e := h.envelope("run", "tg-1.2", "--minutes", "30"); e.Error == nil || e.Error.Code != "usage" {
		t.Fatalf("--minutes without --override: %+v", e.Error)
	}
	if code, e := h.envelope("run", "tg-1.2", "--override", "--minutes", "30"); code != 1 || e.Error == nil ||
		e.Error.Code != "no_terminal" {
		t.Fatalf("no terminal: exit %d %+v", code, e.Error)
	}
}
