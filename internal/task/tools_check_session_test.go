package task

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

func TestQASessionRunsAnItemsCommandInTheQAsSandbox(t *testing.T) {
	r := newPipeRig(t, rigOpts{
		chain: []string{string(llm.ClaudeCLI)},
		items: []agent.Item{
			{ID: "acc-1", Text: "the command passes", Cmd: "echo from-the-sandbox; pwd"},
			{ID: "acc-2", Text: "the command fails", Cmd: "echo nope; exit 4"},
		},
	})
	routes := NewToolRoutes()
	r.m.c.Tools, r.m.c.Socket = routes, "/h/state/engine.sock"

	var pass, fail, unknown, runDef string

	r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
	r.model.on("qa", func(s llm.Session) (llm.Stream, error) {
		ctx := context.Background()
		pass, _ = routes.Call(ctx, s.ID, "run_check", json.RawMessage(`{"item":"acc-1"}`))
		fail, _ = routes.Call(ctx, s.ID, "run_check", json.RawMessage(`{"item":"acc-2"}`))
		unknown, _ = routes.Call(ctx, s.ID, "run_check", json.RawMessage(`{"item":"acc-7"}`))

		defs, _ := routes.Definitions(s.ID)
		for _, d := range defs {
			if d.Name == "run" {
				runDef = string(d.InputSchema)
			}
		}

		return sayResult(
			`{"items":[{"item":"acc-1","pass":true,"confidence":90,` +
				`"evidence":[{"cmd":"echo from-the-sandbox; pwd","exit":0,"excerpt":"from-the-sandbox"}]},` +
				`{"item":"acc-2","pass":true,"confidence":90,` +
				`"evidence":[{"cmd":"echo nope; exit 4","exit":4,"excerpt":"nope"}]}],` +
				`"tasks":[{"task":"t1","confirmation":"confirmed"}]}`,
		)(s)
	})
	r.model.on("reviewer", sayResult(reviewerMet))
	r.reachCleanup()

	if !strings.HasPrefix(pass, "from-the-sandbox\n") {
		t.Errorf("acc-1 reply = %q", pass)
	}
	if fail != "exit status 4\nnope\n" {
		t.Errorf("acc-2 reply = %q", fail)
	}
	if !strings.Contains(unknown, "acc-1, acc-2") {
		t.Errorf("acc-7 reply = %q", unknown)
	}
	if !strings.Contains(runDef, `"enum":["build","test"]`) {
		t.Errorf("run schema = %s", runDef)
	}
}
