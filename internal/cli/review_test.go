package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// ConnCount returns how many connections the fake engine accepted.
func (s *fakeServer) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func TestAgentModelShowsItsWeight(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	_, e := h.envelope("config", "get", "agents.builder.model")
	it := dataItems(t, e)[0]
	if it.Value != "weight:standard" || it.Source != "built-in" {
		t.Fatalf("item = %+v", it)
	}
	if code, e := h.envelope("config", "set", "agents.builder.model=m-1", "agents.builder.session_idle_min=5"); code != 0 {
		t.Fatalf("set: exit %d, %+v", code, e.Error)
	}
	h.stdin = "n\n"
	_, _, errOut := h.run("--json", "config", "set", "--default", "builder")
	if !strings.Contains(errOut, "-> weight:standard") {
		t.Fatalf("prompt = %q", errOut)
	}
}

func TestUnreadableSettingsAreNotATamper(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	if err := os.WriteFile(filepath.Join(h.home, "settings.json"), []byte(`{"limits": {`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, e := h.envelope("config", "set", "--accept"); code != 0 {
		t.Fatalf("accept: exit %d, %+v", code, e.Error)
	}
	repo := repoDir(t, "")
	planFixture(t, h, repo, samplePlan, "tg-1")
	h.fake(answerRunID)
	const warn = "settings file settings.json is unreadable; built-in values are served. Fix: komodo config set --default " +
		"settings.json"
	for _, args := range [][]string{{"config", "get", "limits.group_retries"}, {"stop", samplePlan}, {"run", samplePlan}} {
		code, out, errOut := h.run(append([]string{"--json", "-C", repo}, args...)...)
		if code != 0 || strings.Count(errOut, warn) != 1 || strings.Count(out, `"v"`) != 1 {
			t.Errorf("%v: exit %d\nstdout=%s\nstderr=%s", args, code, out, errOut)
		}
	}
	if code, out, errOut := h.run("--json", "config", "set", "--default", "settings.json"); code != 0 {
		t.Fatalf("repair: exit %d\n%s\n%s", code, out, errOut)
	}
	if _, _, errOut := h.run("--json", "config", "get", "limits.group_retries"); errOut != "" {
		t.Fatalf("still warns after repair: %s", errOut)
	}
}

func TestEngineCodeSetsKindAndExit(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	code := "plan_blocked"
	h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(engine.Reply{Status: engine.StatusRefused, Code: code, Detail: "because " + code, Final: true})
	})
	for c, want := range map[string]int{
		"plan_blocked": 3, "usage": 2, "plan_input": 2, "unknown_target": 1, "locked": 1,
		"internal": 4, "plan_write": 4, "bad_frame": 4, "bogus": 4, "bus_overflow": 4,
	} {
		code = c
		got, e := h.envelope("-C", repo, "clean")
		if got != want || e.Error == nil || !strings.Contains(e.Error.Reason, "because "+c) {
			t.Errorf("%s: exit %d, want %d (%+v)", c, got, want, e.Error)
		}
	}
}

func TestAcceptDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	h.envelope("config", "set", "limits.group_retries=2")
	settings := filepath.Join(h.home, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"limits.group_retries": 4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := filepath.Join(h.home, "state", "config.sha")
	before, _ := os.ReadFile(sha)
	code, e := h.envelope("config", "set", "--accept", "--dry-run")
	d := dataOf[ConfigSetData](t, e)
	if code != 0 || len(d.Changes) != 1 || d.Changes[0].Key != "settings.json" || d.Batch != "" {
		t.Fatalf("exit %d, %+v", code, e)
	}
	if o, n := d.Changes[0].Old, d.Changes[0].New; o == nil || n == nil || o == n {
		t.Fatalf("the preview should show the old and new checksum, got %v and %v", o, n)
	}
	after, _ := os.ReadFile(sha)
	if string(before) != string(after) {
		t.Fatal("--dry-run rewrote config.sha")
	}
	if _, _, errOut := h.run("--json", "config", "get", "limits.group_retries"); !strings.Contains(errOut, "--accept") {
		t.Fatal("the dry run accepted the edit")
	}
}

func TestOneDialPerCommand(t *testing.T) {
	h := newHarness(t)
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(CleanData{})) })
	h.envelope("clean")
	if n := srv.ConnCount(); n != 1 {
		t.Fatalf("the command dialled %d times, want 1", n)
	}
}

func TestOnlyAMissingEngineIsStarted(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, "state"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(home, "state"), 0o755) })
	e := quickEngine(home)
	started := false
	e.Start = startRecorder(&started)
	_, err := e.Send(t.Context(), engine.Request{ID: "x", Verb: engine.VerbClean})
	if err == nil || started || strings.Contains(err.Error(), "no engine answered") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, started = %v", err, started)
	}
}

// startRecorder returns a Start that records it was called.
func startRecorder(flag *bool) func(context.Context) error {
	return func(context.Context) error {
		*flag = true
		return nil
	}
}

func TestLongSocketPathIsRefused(t *testing.T) {
	e := quickEngine(filepath.Join(shortHome(t), strings.Repeat("a", 110)))
	started := false
	e.Start = startRecorder(&started)
	_, err := e.Send(t.Context(), engine.Request{ID: "x", Verb: engine.VerbClean})
	if err == nil || !strings.Contains(err.Error(), "KOMODO_HOME") || started {
		t.Fatalf("err = %v, started = %v", err, started)
	}
}

func TestInternalEngineVerbIsHiddenAndFailsFast(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("internal", "engine")
	if code != 4 || !strings.Contains(e.Error.Reason, "the engine is not built yet") {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	_, out, _ := h.run("--help", "--json")
	if strings.Contains(out, "internal") {
		t.Fatal("the hidden verb is in the help")
	}
}

func TestScheduleCarriesTheRunStart(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	planFixture(t, h, repo, samplePlan, "tg-1", "tg-2")
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(okReply(map[string]any{"run_id": sampleRun}))
	})
	h.envelope("-C", repo, "run", samplePlan, "--schedule", "2026-10-16T02:00:00-07:00")
	ev := srv.Requests()[0].Event
	if ev == nil || ev.Type != bus.TypeRunStart {
		t.Fatalf("event = %+v", ev)
	}
	rs := ev.Data.(*bus.RunStart)
	if rs.Target != samplePlan || rs.Groups == nil || rs.Source != "cli" || rs.Schedule.IsZero() || rs.Root != repo ||
		rs.CommonDir == "" || rs.Repo == "" {
		t.Fatalf("run-start = %+v", rs)
	}
}

func TestStatusTimeoutBelowOneSecond(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	for _, d := range []string{"500ms", "0s"} {
		code, e := h.envelope("-C", repo, "status", "--wait", "--timeout", d)
		if code != 2 || e.Error.Code != "usage" || !strings.Contains(e.Error.Reason, "at least 1s") {
			t.Errorf("%s: exit %d, %+v", d, code, e.Error)
		}
	}
}

func TestDoubleDashEndsFlags(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(map[string]any{})) })
	code, e := h.envelope("-C", repo, "plan", "--from", "doc.md", "--", "-a", "-b")
	if code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	pr := srv.Requests()[0].Event.Data.(*planRequest)
	if pr.Goal != "-a -b" || len(pr.Docs) != 1 || pr.Docs[0] != "doc.md" {
		t.Fatalf("plan = %+v", pr)
	}
}
