package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

func TestBuilderJudgeRejectsABrokenResultAndTurnsNeedsScopeIntoAnOutcome(t *testing.T) {
	b := testRoles(t)["builder"]
	var ge *GateError
	if _, err := b.Judge(context.Background(), result(`{"status":"maybe"}`)); !errors.As(err, &ge) || !ge.Schema {
		t.Fatalf("broken result: %v", err)
	}
	out, err := b.Judge(context.Background(), result(needsScope("lib/a.go")))
	if err != nil || out.Kind != OutcomeScope || len(out.Paths) != 1 {
		t.Fatalf("needs_scope: %+v, %v", out, err)
	}
}

func TestTesterJudgeDecidesFromGosRerunsOfAFailedItem(t *testing.T) {
	failed := result(
		`{"items":[{"item":"a1","pass":false,"evidence":"no"}],"tasks":[{"task":"t1","confirmation":"confirmed"}]}`,
	)
	for _, tc := range []struct {
		name  string
		exits []int
		want  OutcomeKind
	}{
		{"both reruns fail", []int{1, 1}, OutcomeReturn},
		{"both reruns pass", []int{0, 0}, OutcomeRestart},
		{"reruns split", []int{0, 1}, OutcomeBlock},
	} {
		out, err := testRoles(t, tc.exits...)["tester"].Judge(context.Background(), failed)
		if err != nil || out.Kind != tc.want {
			t.Fatalf("%s: %+v, %v", tc.name, out, err)
		}
	}
	var ge *GateError
	_, err := testRoles(t)["tester"].Judge(context.Background(), result(`{"items":[],"tasks":[]}`))
	if !errors.As(err, &ge) {
		t.Fatalf("an incomplete result passed: %v", err)
	}
}

func TestReviewerJudgeKeepsOnlyFindingsVerifiedAgainstTheWork(t *testing.T) {
	roles := testRoles(t)
	rv := roles["reviewer"]
	wt := rv.(*reviewerRole).cfg.Worktree
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a\nvar x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rv.Slots(context.Background(), RoleInput{
		Diff: "+++ b/a.go\n@@ -0,0 +1,2 @@\n+package a\n+var x = 1\n",
	}); err != nil {
		t.Fatal(err)
	}
	rv.(FindingSink).Submit(Submitted{})
	out, err := rv.Judge(context.Background(), result(`{"verdicts":[{"task":"t1","verdict":"met"}]}`))
	if err != nil || out.Kind != OutcomePass {
		t.Fatalf("an unverifiable finding blocked a met review: %+v, %v", out, err)
	}
	out, err = rv.Judge(context.Background(), result(`{"verdicts":[{"task":"t1","verdict":"brief_incomplete"}]}`))
	if err != nil || out.Kind != OutcomeBlock || out.Reason != agent.BlockBrief {
		t.Fatalf("brief_incomplete: %+v, %v", out, err)
	}
}

// testRoles builds the three roles over a runner that answers each rerun from a script of exit codes.
func testRoles(t *testing.T, exits ...int) map[string]Role {
	t.Helper()
	wt := t.TempDir()
	n := 0
	roles, err := NewRoles(RoleConfig{
		Group: "tg-1", Tasks: []TaskSpec{{ID: "t1", Title: "x", DoneWhen: "build"}}, Worktree: wt,
		Items: []agent.Item{{ID: "a1", Text: "works", Cmd: "test"}},
		Files: func() []string { return []string{"*.go"} },
		Runner: func() agent.CommandRunner {
			return func(context.Context, string) (int, string, error) {
				e := exits[n%len(exits)]
				n++
				return e, "out", nil
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return roles
}

func result(s string) llm.Result { return llm.Result{Output: json.RawMessage(s)} }
