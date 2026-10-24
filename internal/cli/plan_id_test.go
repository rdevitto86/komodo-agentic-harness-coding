package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

const samplePlan = "plan-7f3a9c"

// planFixture writes the blueprint of a plan with the given group labels into the harness home, for the repository.
func planFixture(t *testing.T, h *harness, repo, plan string, labels ...string) {
	t.Helper()
	b := intake.Blueprint{Plan: plan, Repo: intake.RepoFolder(RepoID(repo)), Goal: "g"}
	for _, l := range labels {
		b.Groups = append(b.Groups, intake.PlanGroup{ID: "id-" + l, Name: l})
	}
	if err := intake.WriteBlueprint(h.home, b); err != nil {
		t.Fatal(err)
	}
}

// planThenRun answers a plan request with the written plan and everything else with a run id.
func planThenRun(srv *fakeServer, req engine.Request, send func(engine.Reply)) {
	if req.Verb == engine.VerbPlan {
		send(okReply(map[string]any{"plan": samplePlan, "path": "/h/plans/r/" + samplePlan + "/plan.md"}))
		return
	}
	answerRunID(srv, req, send)
}

func TestPlanIDCommands(t *testing.T) {
	t.Run("Label resolution", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		planFixture(t, h, repo, samplePlan, "tg-1", "tg-2")
		srv := h.fake(answerRunID)

		code, e := h.envelope("-C", repo, "run", samplePlan)
		if code != 0 || !e.OK {
			t.Fatalf("run plan: exit %d, %+v", code, e)
		}
		rs := srv.Requests()[0].Event.Data.(*bus.RunStart)
		if rs.Target != samplePlan || len(rs.Groups) != 0 || srv.Requests()[0].Target != samplePlan {
			t.Fatalf("run-start = %+v", rs)
		}

		if code, e = h.envelope("-C", repo, "run", "plan-000000"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("unknown plan: exit %d, %+v", code, e.Error)
		}
		if code, e = h.envelope("-C", repo, "run", samplePlan, "tg-9", "x"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("unknown group: exit %d, %+v", code, e.Error)
		}
		if code, e = h.envelope("-C", repo, "run", samplePlan, "tg-2"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("group with no prompt: exit %d, %+v", code, e.Error)
		}
		if code, e = h.envelope("-C", repo, "run", "--all"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("run --all: exit %d, %+v", code, e.Error)
		}
		if n := len(srv.Requests()); n != 1 {
			t.Fatalf("refused commands reached the engine: %d requests", n)
		}
	})

	t.Run("Answer flags", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		planFixture(t, h, repo, samplePlan, "tg-1", "tg-2")
		srv := h.fake(func(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
			if rc, ok := req.Event.Data.(*bus.RunContinue); ok && len(rc.Groups) == 1 && rc.Groups[0] == "tg-1" {
				send(engine.Reply{Status: engine.StatusRefused, Code: "not_blocked", Detail: "tg-1 is not blocked", Final: true})
				return
			}
			send(okReply(map[string]any{}))
		})

		if code, e := h.envelope("-C", repo, "run", samplePlan, "--approve", "--deny"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("approve and deny: exit %d, %+v", code, e.Error)
		}
		if code, e := h.envelope("-C", repo, "run", samplePlan, "--approve"); code != 0 {
			t.Fatalf("approve all: exit %d, %+v", code, e)
		}
		if code, e := h.envelope("-C", repo, "run", samplePlan, "tg-2", "--deny"); code != 0 {
			t.Fatalf("deny tg-2: exit %d, %+v", code, e)
		}
		code, e := h.envelope("-C", repo, "run", samplePlan, "tg-1", "--approve")
		if code != 1 || e.Error.Code != "not_blocked" {
			t.Fatalf("not blocked: exit %d, %+v", code, e.Error)
		}

		reqs := srv.Requests()
		all := reqs[0].Event.Data.(*bus.RunContinue)
		one := reqs[1].Event.Data.(*bus.RunContinue)
		if all.Target != samplePlan || len(all.Groups) != 0 || all.Answer != "approve" || !reqs[0].Flags.Approve {
			t.Fatalf("approve all = %+v", all)
		}
		if len(one.Groups) != 1 || one.Groups[0] != "tg-2" || one.Answer != "deny" || !reqs[1].Flags.Deny {
			t.Fatalf("deny tg-2 = %+v", one)
		}
		if code, e = h.envelope("-C", repo, "run", samplePlan, "tg-2", "do it", "--approve"); code != 2 {
			t.Fatalf("approve with a prompt: exit %d, %+v", code, e.Error)
		}
		if code, e = h.envelope("-C", repo, "run", samplePlan, "--approve", "--schedule", "2026-10-16T02:00:00-07:00"); code != 2 {
			t.Fatalf("approve with schedule: exit %d, %+v", code, e.Error)
		}
	})

	t.Run("Group rewrite and prompt words", func(t *testing.T) {
		const prompt = "use the cache instead of the database"
		h := newHarness(t)
		repo := repoDir(t, "")
		planFixture(t, h, repo, samplePlan, "tg-1", "tg-2")
		srv := h.fake(planThenRun)

		for _, words := range [][]string{{prompt}, strings.Fields(prompt)} {
			args := append([]string{"-C", repo, "run", samplePlan, "tg-2"}, words...)
			if code, e := h.envelope(args...); code != 0 || !e.OK {
				t.Fatalf("rewrite: exit %d, %+v", code, e)
			}
		}

		reqs := srv.Requests()
		if len(reqs) != 4 {
			t.Fatalf("requests = %d, want 4", len(reqs))
		}
		for i := 0; i < 4; i += 2 {
			pr := reqs[i].Event.Data.(*planRequest)
			if reqs[i].Verb != engine.VerbPlan || pr.Plan != samplePlan || pr.Group != "tg-2" || pr.Goal != prompt {
				t.Fatalf("plan request %d = %+v", i, pr)
			}
			rs := reqs[i+1].Event.Data.(*bus.RunStart)
			if reqs[i+1].Verb != engine.VerbRun || rs.Target != samplePlan || len(rs.Groups) != 1 || rs.Groups[0] != "tg-2" {
				t.Fatalf("run-start %d = %+v", i+1, rs)
			}
		}
	})

	t.Run("Group rewrite stops when planning fails", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		planFixture(t, h, repo, samplePlan, "tg-2")
		srv := h.fake(func(_ *fakeServer, req engine.Request, send func(engine.Reply)) {
			send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{
				Type: bus.TypeError, Subtype: "plan-input", Run: req.Event.Run, Data: &intake.PlanInput{Reason: "bad prompt"},
			}})
			send(okReply(nil))
		})

		code, e := h.envelope("-C", repo, "run", samplePlan, "tg-2", "do it")
		if code != 2 || e.Error.Code != "plan_input" {
			t.Fatalf("exit %d, %+v", code, e.Error)
		}
		if n := len(srv.Requests()); n != 1 {
			t.Fatalf("run-start followed a failed plan: %d requests", n)
		}
	})

	t.Run("bad plans", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		planFixture(t, h, repo, samplePlan, "tg-2")
		h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(map[string]any{})) })

		if code, e := h.envelope("-C", repo, "run", samplePlan, "tg-2", "do it"); code != 4 || e.Error.Code != "internal" {
			t.Fatalf("planning that wrote nothing: exit %d, %+v", code, e.Error)
		}
		if code, e := h.envelope("-C", repo, "run", "../x"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("path in a plan id: exit %d, %+v", code, e.Error)
		}

		bad := filepath.Join(h.home, "plans", intake.RepoFolder(RepoID(repo)), "plan-bad000", "blueprint.json")
		if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, e := h.envelope("-C", repo, "run", "plan-bad000"); code != 4 || e.Error.Code != "internal" {
			t.Fatalf("corrupt blueprint: exit %d, %+v", code, e.Error)
		}
	})

	t.Run("plan", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		planFixture(t, h, repo, samplePlan, "tg-1")
		srv := h.fake(planThenRun)

		code, e := h.envelope("-C", repo, "plan", "add", "a", "cache")
		if code != 0 {
			t.Fatalf("goal: exit %d, %+v", code, e)
		}
		if d := dataOf[PlanData](t, e); d.Plan != samplePlan || d.Path != "/h/plans/r/"+samplePlan+"/plan.md" {
			t.Fatalf("data = %+v", d)
		}
		if pr := srv.Requests()[0].Event.Data.(*planRequest); pr.Goal != "add a cache" || pr.Plan != "" || pr.Group != "" {
			t.Fatalf("goal request = %+v", pr)
		}

		if code, e = h.envelope("-C", repo, "plan", samplePlan, "split", "tg-1"); code != 0 {
			t.Fatalf("shape change: exit %d, %+v", code, e)
		}
		if pr := srv.Requests()[1].Event.Data.(*planRequest); pr.Plan != samplePlan || pr.Group != "" || pr.Goal != "split tg-1" {
			t.Fatalf("shape request = %+v", pr)
		}

		if code, e = h.envelope("-C", repo, "plan", samplePlan); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("plan id with no prompt: exit %d, %+v", code, e.Error)
		}
		if code, e = h.envelope("-C", repo, "plan", "plan-000000", "split"); code != 2 || e.Error.Code != "usage" {
			t.Fatalf("unknown plan id: exit %d, %+v", code, e.Error)
		}
	})

	t.Run("targets", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(map[string]any{})) })

		for _, args := range [][]string{
			{"stop", samplePlan}, {"stop", samplePlan, "tg-2"}, {"stop", sampleRun},
			{"replay", samplePlan, "tg-2"}, {"abandon", samplePlan, "tg-2"}, {"status", samplePlan, "tg-2"},
		} {
			if code, e := h.envelope(append([]string{"-C", repo}, args...)...); code != 0 {
				t.Fatalf("%v: exit %d, %+v", args, code, e)
			}
		}
		var got []string
		for _, r := range srv.Requests() {
			got = append(got, r.Target)
		}
		want := "plan-7f3a9c|plan-7f3a9c tg-2|" + sampleRun + "|plan-7f3a9c tg-2|plan-7f3a9c tg-2|plan-7f3a9c tg-2"
		if strings.Join(got, "|") != want {
			t.Fatalf("targets = %q", got)
		}
		if rs := srv.Requests()[1].Event.Data.(*bus.RunStop); rs.Target != "plan-7f3a9c tg-2" {
			t.Fatalf("run-stop = %+v", rs)
		}

		for _, args := range [][]string{
			{"stop", "tg-2"}, {"replay", "tg-1.2"}, {"status", "tg-2"}, {"abandon", "tg-2"}, {"abandon", samplePlan},
			{"stop"}, {"stop", samplePlan, "tg-1", "extra"}, {"stop", sampleRun, "tg-1"}, {"abandon", sampleRun, "tg-1"},
		} {
			if code, e := h.envelope(append([]string{"-C", repo}, args...)...); code != 2 || e.Error.Code != "usage" {
				t.Fatalf("%v: exit %d, %+v", args, code, e.Error)
			}
		}
	})

	t.Run("run id answers", func(t *testing.T) {
		h := newHarness(t)
		repo := repoDir(t, "")
		srv := h.fake(func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) { send(okReply(map[string]any{})) })

		if code, e := h.envelope("-C", repo, "run", sampleRun, "--approve"); code != 0 {
			t.Fatalf("exit %d, %+v", code, e)
		}
		ev := srv.Requests()[0].Event
		if rc := ev.Data.(*bus.RunContinue); rc.Target != sampleRun || ev.Run != sampleRun {
			t.Fatalf("run-continue = %+v, run %q", rc, ev.Run)
		}
	})
}

func TestListPlanIDs(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	planFixture(t, h, repo, "plan-aaaaaa", "tg-1")
	planFixture(t, h, repo, "plan-bbbbbb", "tg-1")
	hidden := filepath.Join(h.home, "plans", intake.RepoFolder(RepoID(repo)), ".plan-stage")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"run", "plan"} {
		code, e := h.envelope("-C", repo, cmd, "--list")
		if code != 0 || !e.OK {
			t.Fatalf("%s --list: exit %d, %+v", cmd, code, e)
		}
		b, _ := json.Marshal(e.Data)
		if string(b) != `{"plans":["plan-aaaaaa","plan-bbbbbb"]}` || !strings.Contains(e.Report, "plan-bbbbbb") {
			t.Fatalf("%s --list data %s, report %q", cmd, b, e.Report)
		}
	}

	h.terminal = true
	code, out, _ := h.run("-C", repo, "run", "--list")
	if code != 0 || strings.TrimSpace(out) != "plan-aaaaaa\nplan-bbbbbb" {
		t.Fatalf("text list: exit %d, %q", code, out)
	}

	other := repoDir(t, "")
	code, out, _ = h.run("-C", other, "plan", "--list")
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("empty list: exit %d, %q", code, out)
	}

	if code, _ := h.envelope("-C", repo, "stop", "--list"); code != 4 {
		t.Fatalf("stop --list: exit %d, want 4", code)
	}
}
