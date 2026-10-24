package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func qaResult(pass bool, confidence string) llm.Result {
	return result(
		`{"status":"tested","items":[{"item":"a1","pass":` + strconv.FormatBool(pass) + confidence +
			`,"evidence":[{"cmd":"test","exit":1,"excerpt":"no"}]}],` +
			`"tasks":[{"task":"t1","confirmation":"confirmed"}]}`,
	)
}

// An item Quality Assurance marks failed returns to Build whatever its confidence; Go reruns nothing.
func TestQAJudgeReturnsAFailedItemWhateverItsConfidence(t *testing.T) {
	for _, conf := range []string{`,"confidence":0`, `,"confidence":50`, `,"confidence":100`} {
		out, err := testRoles(t)["qa"].Judge(context.Background(), qaResult(false, conf))
		if err != nil || out.Kind != OutcomeReturn || len(out.Findings) != 1 {
			t.Fatalf("%s: %+v, %v", conf, out, err)
		}
	}

	out, err := testRoles(t)["qa"].Judge(context.Background(), qaResult(true, `,"confidence":90`))
	if err != nil || out.Kind != OutcomePass || !strings.Contains(strings.Join(out.Summary, "\n"), "a1 90") {
		t.Fatalf("a passed item: %+v, %v", out, err)
	}
}

// A passed item signs off only at confidence 80 or more; below that the group returns to Build as for a failed item.
func TestQAJudgeSignsOffAPassedItemOnlyAtConfidence80(t *testing.T) {
	out, err := testRoles(t)["qa"].Judge(context.Background(), qaResult(true, `,"confidence":79`))
	if err != nil || out.Kind != OutcomeReturn || len(out.Findings) != 1 ||
		!strings.Contains(out.Findings[0], "a1") || !strings.Contains(out.Findings[0], "79") {
		t.Fatalf("a pass at 79: %+v, %v", out, err)
	}

	out, err = testRoles(t)["qa"].Judge(context.Background(), qaResult(true, `,"confidence":80`))
	if err != nil || out.Kind != OutcomePass {
		t.Fatalf("a pass at 80: %+v, %v", out, err)
	}
}

func TestQAJudgeRefusesAnItemWithoutAConfidence(t *testing.T) {
	var ge *GateError
	for _, conf := range []string{``, `,"confidence":101`, `,"confidence":-1`} {
		_, err := testRoles(t)["qa"].Judge(context.Background(), qaResult(true, conf))
		if !errors.As(err, &ge) {
			t.Fatalf("%q: result accepted: %v", conf, err)
		}
	}

	_, err := testRoles(t)["qa"].Judge(context.Background(), result(`{"status":"tested","items":[],"tasks":[]}`))
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

// testRoles builds the three roles over a temporary worktree.
func testRoles(t *testing.T) map[string]Role {
	t.Helper()
	wt := t.TempDir()
	roles, err := NewRoles(RoleConfig{
		Group: "tg-1", Tasks: []TaskSpec{{ID: "t1", Title: "x", DoneWhen: "build"}}, Worktree: wt,
		Items: []agent.Item{{ID: "a1", Text: "works", Cmd: "test"}},
		Files: func() []string { return []string{"*.go"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	return roles
}

func result(s string) llm.Result { return llm.Result{Output: json.RawMessage(s)} }

// A group whose tasks carry no acceptance criteria still gets a Quality Assurance brief; the acceptance section reads none.
func TestQASlotsFillAnEmptyAcceptanceSectionWhenTheGroupHasNoCriteria(t *testing.T) {
	roles, err := NewRoles(RoleConfig{
		Group: "tg-1", Tasks: []TaskSpec{{ID: "t1", Title: "x", DoneWhen: "build"}}, Worktree: t.TempDir(),
		Files: func() []string { return []string{"*.go"} },
	})
	if err != nil {
		t.Fatal(err)
	}

	slots, err := roles["qa"].Slots(context.Background(), RoleInput{})
	if err != nil || slots["acceptance"] != "none" {
		t.Fatalf("acceptance slot %q, %v", slots["acceptance"], err)
	}

	slots, err = testRoles(t)["qa"].Slots(context.Background(), RoleInput{})
	if err != nil || !strings.Contains(slots["acceptance"], "a1: works") {
		t.Fatalf("acceptance slot %q, %v", slots["acceptance"], err)
	}
}
