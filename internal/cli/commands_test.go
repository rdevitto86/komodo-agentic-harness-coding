package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

func init() {
	bus.Register(bus.Key{Type: bus.TypePlan, Subtype: "request"}, func() bus.Payload { return &planRequest{} })
}

// fake starts a fake engine server on the harness home and points the CLI at it.
func (h *harness) fake(handler func(*fakeServer, engine.Request, func(engine.Reply))) *fakeServer {
	srv := startFake(h.t, h.home, handler)
	e := quickEngine(h.home)
	e.Start = func(context.Context) error { return errors.New("the test engine cannot be started") }
	h.engine = e
	return srv
}

// dataOf decodes an envelope's data into T.
func dataOf[T any](t *testing.T, e Envelope) T {
	t.Helper()
	b, _ := json.Marshal(e.Data)
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("data %s: %v", b, err)
	}
	return v
}

// backlog writes TG.md files under docs/backlog/ of a repository.
func backlog(t *testing.T, repo string, groups map[string]string) {
	t.Helper()
	for path, status := range groups {
		dir := filepath.Join(repo, "docs", "backlog", path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "TG.md"), []byte("goal: x\nstatus: "+status+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const sampleRun = "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57"

// answerRunID replies ok with the run id of the request's event.
func answerRunID(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
	id := sampleRun
	if req.Event != nil && req.Event.Run != "" {
		id = string(req.Event.Run)
	}
	send(okReply(map[string]any{"run_id": id}))
}

func TestRunSendsRunStartEvent(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	backlog(t, repo, map[string]string{"epic-1/tg-1.2": "todo"})
	srv := h.fake(answerRunID)
	code, e := h.envelope("-C", repo, "run", "tg-1.2")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Verb != engine.VerbRun || reqs[0].Target != "tg-1.2" || reqs[0].Root != repo {
		t.Fatalf("requests = %+v", reqs)
	}
	ev := reqs[0].Event
	rs, ok := ev.Data.(*bus.RunStart)
	if ev.Type != bus.TypeRunStart || !ok || !uuidPattern.MatchString(string(ev.Run)) {
		t.Fatalf("event = %+v", ev)
	}
	if rs.Target != "tg-1.2" || len(rs.Groups) != 1 || rs.Groups[0] != "tg-1.2" || rs.Source != "cli" || rs.Root != repo ||
		rs.CommonDir != filepath.Join(repo, ".git") || string(rs.Request) != reqs[0].ID || rs.Repo == "" {
		t.Fatalf("run-start data = %+v", rs)
	}
	if got := dataOf[RunData](t, e); got.RunID != string(ev.Run) || !strings.Contains(e.Report, string(ev.Run)) {
		t.Fatalf("data = %+v, report = %q", got, e.Report)
	}
}

func TestRunLabelResolution(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	backlog(t, repo, map[string]string{
		"epic-1/tg-1.1": "done", "epic-1/tg-1.2": "todo", "epic-1/tg-1.10": "todo", "epic-2/tg-2.1": "in_progress",
	})
	srv := h.fake(answerRunID)
	h.envelope("-C", repo, "run", "ep-1")
	h.envelope("-C", repo, "run", "--all")
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if g := reqs[0].Event.Data.(*bus.RunStart).Groups; strings.Join(g, ",") != "tg-1.1,tg-1.2,tg-1.10" {
		t.Fatalf("ep-1 groups = %v", g)
	}
	rs := reqs[1].Event.Data.(*bus.RunStart)
	if rs.Target != "all" || strings.Join(rs.Groups, ",") != "tg-1.2,tg-1.10,tg-2.1" || !reqs[1].Flags.All {
		t.Fatalf("--all = %+v, flags %+v", rs, reqs[1].Flags)
	}
	code, e := h.envelope("-C", repo, "run", "ep-9")
	if code != 2 || e.Error.Code != "usage" || len(srv.Requests()) != 2 {
		t.Fatalf("ep-9: exit %d, %+v, requests %d", code, e.Error, len(srv.Requests()))
	}
	if code, e = h.envelope("-C", repo, "run", "tg-9.9"); code != 2 {
		t.Fatalf("tg-9.9: exit %d, %+v", code, e.Error)
	}
}

func TestRunNeedsRepository(t *testing.T) {
	h := newHarness(t)
	srv := h.fake(answerRunID)
	code, e := h.envelope("-C", t.TempDir(), "run", "tg-1.2")
	if code != 1 || e.Error.Code != "no_repo" || len(srv.Requests()) != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
}

func TestRunAnswerFlags(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	var refuse bool
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		if refuse {
			send(engine.Reply{Status: engine.StatusRefused, Code: engine.CodeNotBlocked, Detail: "tg-1.2 is not blocked", Final: true})
			return
		}
		send(okReply(map[string]any{}))
	})
	code, e := h.envelope("-C", repo, "run", "tg-1.2", "--approve")
	if code != 0 || !e.OK {
		t.Fatalf("approve: exit %d, %+v", code, e)
	}
	req := srv.Requests()[0]
	rc, ok := req.Event.Data.(*bus.RunContinue)
	if !req.Flags.Approve || req.Event.Type != bus.TypeRunContinue || !ok || rc.Answer != "approve" || rc.Target != "tg-1.2" {
		t.Fatalf("request = %+v", req)
	}
	refuse = true
	code, e = h.envelope("-C", repo, "run", "tg-1.2", "--deny")
	if code != 1 || e.Error.Code != "not_blocked" || e.OK {
		t.Fatalf("not blocked: exit %d, %+v", code, e)
	}
	n := len(srv.Requests())
	for _, args := range [][]string{
		{"run", "tg-1.2", "--approve", "--deny"}, {"run", "tg-1.2", "--approve", "--all"},
		{"run", "tg-1.2", "--deny", "--schedule", "2026-10-16T02:00:00-07:00"}, {"run", "--all", "tg-1.2"}, {"run"},
	} {
		if code, e := h.envelope(append([]string{"-C", repo}, args...)...); code != 2 || e.Error.Code != "usage" {
			t.Errorf("%v: exit %d, %+v", args, code, e.Error)
		}
	}
	if len(srv.Requests()) != n {
		t.Error("a usage error reached the engine")
	}
}

func TestRunSchedule(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	backlog(t, repo, map[string]string{"epic-1/tg-1.2": "todo"})
	srv := h.fake(func(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
		send(okReply(map[string]any{"run_id": sampleRun, "queued": map[string]any{"target": req.Target, "at": req.Flags.Schedule}}))
	})
	code, e := h.envelope("-C", repo, "run", "tg-1.2", "--schedule", "2026-10-16T02:00:00-07:00")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e)
	}
	req := srv.Requests()[0]
	want, _ := time.Parse(time.RFC3339, "2026-10-16T02:00:00-07:00")
	if req.Event == nil || !req.Flags.Schedule.Equal(want) {
		t.Fatalf("request = %+v", req)
	}
	d := dataOf[RunData](t, e)
	if d.RunID != sampleRun || d.Queued == nil || !d.Queued.At.Equal(want) || !strings.Contains(e.Report, sampleRun) {
		t.Fatalf("data = %+v, report %q", d, e.Report)
	}
	if code, e := h.envelope("-C", repo, "run", "tg-1.2", "--schedule", "tomorrow"); code != 2 || e.Error.Code != "usage" {
		t.Fatalf("tomorrow: exit %d, %+v", code, e.Error)
	}
}

func TestStopAndAbandon(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	refuse := false
	srv := h.fake(func(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
		if refuse {
			send(engine.Reply{Status: engine.StatusRefused, Code: engine.CodeUnknownTarget, Detail: "nothing named " + req.Target, Final: true})
			return
		}
		send(okReply(map[string]any{"run_id": sampleRun}))
	})
	code, e := h.envelope("-C", repo, "stop", "tg-1.2")
	req := srv.Requests()[0]
	if code != 0 || req.Verb != engine.VerbStop || req.Event.Type != bus.TypeRunStop || req.Event.Data.(*bus.RunStop).Target != "tg-1.2" ||
		dataOf[StopData](t, e).RunID != sampleRun {
		t.Fatalf("stop: exit %d, %+v, %+v", code, e, req)
	}
	code, e = h.envelope("-C", repo, "abandon", "tg-1.2")
	if code != 0 || srv.Requests()[1].Verb != engine.VerbAbandon || srv.Requests()[1].Target != "tg-1.2" || srv.Requests()[1].Event != nil {
		t.Fatalf("abandon: exit %d, %+v", code, e)
	}
	if code, e = h.envelope("-C", repo, "abandon", "ep-1"); code != 2 || e.Error.Code != "usage" {
		t.Fatalf("abandon epic: exit %d, %+v", code, e.Error)
	}
	refuse = true
	if code, e = h.envelope("-C", repo, "stop", "tg-8.8"); code != 1 || e.Error.Code != "unknown_target" {
		t.Fatalf("unknown target: exit %d, %+v", code, e.Error)
	}
	if code, e = h.envelope("-C", repo, "abandon", "tg-8.8"); code != 1 || e.Error.Code != "unknown_target" {
		t.Fatalf("abandon unknown: exit %d, %+v", code, e.Error)
	}
}

func TestPlanWaitsForRunStart(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{
			Type: bus.TypeRunStart, Run: "0192e5a0-3b44-7d10-8c62-9a1f4e7b0d25",
			Data: &bus.RunStart{Target: "ep-2", Groups: []string{"tg-2.1", "tg-2.2"}, Source: "intake"},
		}})
		send(okReply(map[string]any{}))
	})
	code, e := h.envelope("-C", repo, "plan", "Add lease revocation", "--from", "docs/hld.md")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e)
	}
	req := srv.Requests()[0]
	pr, ok := req.Event.Data.(*planRequest)
	if req.Verb != engine.VerbPlan || req.Event.Type != bus.TypePlan || req.Event.Subtype != "request" || !ok ||
		pr.Goal != "Add lease revocation" || len(pr.Docs) != 1 || pr.Docs[0] != "docs/hld.md" || !uuidPattern.MatchString(string(req.Event.Run)) {
		t.Fatalf("request = %+v", req)
	}
	d := dataOf[PlanData](t, e)
	if d.Target != "ep-2" || strings.Join(d.Groups, ",") != "tg-2.1,tg-2.2" || d.RunID != "0192e5a0-3b44-7d10-8c62-9a1f4e7b0d25" {
		t.Fatalf("data = %+v", d)
	}
	if code, e := h.envelope("-C", repo, "plan"); code != 2 {
		t.Fatalf("empty plan: exit %d, %+v", code, e.Error)
	}
}

func TestPlanFailureIsTheErrorEvent(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{Type: bus.TypeError, Subtype: "fault.error", Data: &fault.ErrorFault{FaultData: fault.FaultData{
			Component: "intake", Code: "plan_blocked", Kind: "blocked", Severity: "error", Reason: "The third planner draft failed lint",
		}}}})
		send(okReply(map[string]any{}))
	})
	code, e := h.envelope("-C", repo, "plan", "goal text")
	if code != 3 || e.OK || e.Error == nil || e.Error.Code != "plan_blocked" {
		t.Fatalf("exit %d, %+v", code, e)
	}
}

func TestReplayCollectsEventsAndGaps(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{Type: bus.TypeRunStatus, Subtype: "run.start", Run: sampleRun, Data: &bus.RunStartedFact{Groups: []bus.ID{"g1"}, ConfigHash: "h", Version: "v"}}})
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{Type: bus.TypeRunComplete, Subtype: "stopped", Run: sampleRun, Data: &bus.RunStopped{}}})
		send(okReply(map[string]any{"gaps": 1}))
	})
	code, e := h.envelope("-C", repo, "replay", "tg-1.2")
	d := dataOf[ReplayData](t, e)
	if code != 0 || len(d.Steps) != 2 || d.Gaps != 1 || d.RunID != sampleRun || d.Steps[0].Type != bus.TypeRunStatus || d.Steps[1].Subtype != "stopped" {
		t.Fatalf("exit %d, data %+v", code, d)
	}
	if req := srv.Requests()[0]; req.Verb != engine.VerbReplay || req.Target != "tg-1.2" || req.Event != nil {
		t.Fatalf("request = %+v", req)
	}
	if !strings.Contains(e.Report, "2 steps") || !strings.Contains(e.Report, "run-status") {
		t.Fatalf("report = %q", e.Report)
	}
}

func TestCleanDryRun(t *testing.T) {
	h := newHarness(t)
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(okReply(CleanData{Removed: []CleanItem{{Kind: "worktree", Name: "tg-1.2"}}}))
	})
	code, e := h.envelope("clean", "--dry-run")
	if code != 0 || len(dataOf[CleanData](t, e).Removed) != 1 || !srv.Requests()[0].Flags.DryRun || srv.Requests()[0].Verb != engine.VerbClean {
		t.Fatalf("exit %d, %+v", code, e)
	}
	if !strings.Contains(e.Report, "tg-1.2") {
		t.Fatalf("report = %q", e.Report)
	}
}

func statusFixture() StatusData {
	return StatusData{
		Runs:    []RunRow{{RunID: sampleRun, Target: "ep-1", Groups: []GroupRow{{Group: "tg-1.2", Stage: "build", State: "blocked"}}}},
		Queued:  []QueuedRow{},
		Blocked: []BlockedRow{{Group: "tg-1.2", RunID: sampleRun, Reason: "needs_scope", Question: "The builder needs internal/cache/store.go, outside its scope."}},
	}
}

func TestStatusFiguresAndScorecard(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	srv := h.fake(func(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
		d := statusFixture()
		if req.Target != "" {
			d.Figures = &Figures{WallS: 90, TokensIn: 1000, TokensOut: 200, USD: 0.5, USDEquiv: 1.25}
			d.Explain = []bus.Event{{Type: bus.TypeRunComplete, Subtype: "stopped", Run: sampleRun, Data: &bus.RunStopped{}}}
		}
		if req.Flags.All {
			d.Recent = nil
		}
		send(okReply(d))
	})
	code, e := h.envelope("-C", repo, "status", "tg-1.2")
	d := dataOf[StatusData](t, e)
	req := srv.Requests()[0]
	if code != 0 || req.Verb != engine.VerbStatus || req.Target != "tg-1.2" || req.Root != repo || d.Figures == nil || d.Figures.TokensIn != 1000 || len(d.Explain) != 1 {
		t.Fatalf("status (id): exit %d, req %+v, data %+v", code, req, d)
	}
	if !strings.Contains(e.Report, "1 active run") {
		t.Fatalf("report = %q", e.Report)
	}
	code, e = h.envelope("status", "--all")
	if code != 0 || !srv.Requests()[1].Flags.All || srv.Requests()[1].Root != "" {
		t.Fatalf("status --all: exit %d, %+v", code, srv.Requests()[1])
	}
	code, e = h.envelope("-C", t.TempDir(), "status")
	if code != 1 || e.Error.Code != "no_repo" {
		t.Fatalf("status outside a repository: exit %d, %+v", code, e.Error)
	}
}

func TestStatusWait(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	waited := "blocked"
	srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		if waited == "blocked" {
			b, _ := json.Marshal(statusFixture())
			send(engine.Reply{Status: engine.StatusOK, Data: b})
		}
		send(okReply(map[string]any{"waited": waited}))
	})
	code, e := h.envelope("-C", repo, "status", "--wait")
	d := dataOf[StatusData](t, e)
	if code != 3 || !e.OK || d.Waited != "blocked" || len(d.Blocked) != 1 || !strings.Contains(e.Report, "internal/cache/store.go") {
		t.Fatalf("blocked: exit %d, %+v", code, e)
	}
	if f := srv.Requests()[0].Flags; !f.Wait || f.TimeoutS != 540 {
		t.Fatalf("flags = %+v", f)
	}
	waited = "done"
	code, e = h.envelope("-C", repo, "status", "--wait", "--timeout", "30s")
	if d := dataOf[StatusData](t, e); code != 0 || d.Waited != "done" || srv.Requests()[1].Flags.TimeoutS != 30 {
		t.Fatalf("done: exit %d, %+v", code, e)
	}
	waited = "timeout"
	if code, e = h.envelope("-C", repo, "status", "--wait"); code != 0 || dataOf[StatusData](t, e).Waited != "timeout" {
		t.Fatalf("timeout: exit %d, %+v", code, e)
	}
}

func TestEngineFailuresMapToExitCodes(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	reply := engine.Reply{Status: engine.StatusRefused, Code: engine.CodeRunRefused, Detail: "dependency cycle", Final: true}
	h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(reply) })
	if code, e := h.envelope("-C", repo, "clean"); code != 1 || e.Error.Code != "run_refused" || !strings.Contains(e.Error.Reason, "dependency cycle") {
		t.Fatalf("refused: exit %d, %+v", code, e.Error)
	}
	reply = engine.Reply{Status: engine.StatusError, Detail: "disk full", Final: true}
	if code, e := h.envelope("-C", repo, "clean"); code != 4 || e.Error.Code != "internal" || !strings.Contains(e.Error.Reason, "disk full") {
		t.Fatalf("error: exit %d, %+v", code, e.Error)
	}
}

func TestNoEngineExitsFourNamingSocket(t *testing.T) {
	h := newHarness(t)
	e := quickEngine(h.home)
	e.StartWait = 50 * time.Millisecond
	e.Start = func(context.Context) error { return nil }
	h.engine = e
	code, env := h.envelope("clean")
	if code != 4 || env.Error.Code != "internal" || !strings.Contains(env.Error.Reason, filepath.Join(h.home, "state", "engine.sock")) {
		t.Fatalf("exit %d, %+v", code, env.Error)
	}
}

func TestListFormsOutsideConfigAreNotBuilt(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("run", "--list")
	if code != 4 || !strings.Contains(e.Error.Reason, "not built yet") {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
}

func TestWritingCommandsHonourGuardOnStop(t *testing.T) {
	h := newHarness(t)
	h.fake(answerRunID)
	repo := repoDir(t, "")
	if err := os.WriteFile(filepath.Join(h.home, "settings.json"), []byte(`{"limits.returns": 2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"stop", "tg-1.2"}, {"status"}, {"abandon", "tg-1.2"}, {"replay", "tg-1.2"}, {"clean"}} {
		_, out, errOut := h.run(append([]string{"--json", "-C", repo}, args...)...)
		if !strings.Contains(errOut, "Warning") || strings.Contains(out, "config_tampered") {
			t.Errorf("%v: stdout %s stderr %s", args, out, errOut)
		}
	}
	for _, args := range [][]string{{"plan", "x"}, {"run", "tg-1.2"}} {
		code, e := h.envelope(append([]string{"-C", repo}, args...)...)
		if code != 1 || e.Error.Code != "config_tampered" {
			t.Errorf("%v: exit %d, %+v", args, code, e.Error)
		}
	}
}
