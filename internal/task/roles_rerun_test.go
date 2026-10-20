package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// judgeWithRunner judges one failed item a1 whose reruns go through run.
func judgeWithRunner(t *testing.T, run agent.CommandRunner) Outcome {
	t.Helper()

	roles, err := NewRoles(RoleConfig{
		Group: "tg-1", Tasks: []TaskSpec{{ID: "t1", Title: "x", DoneWhen: "build"}}, Worktree: t.TempDir(),
		Items:  []agent.Item{{ID: "a1", Text: "works", Cmd: "test"}},
		Files:  func() []string { return []string{"*.go"} },
		Runner: func() agent.CommandRunner { return run },
	})
	if err != nil {
		t.Fatal(err)
	}

	failed := result(
		`{"items":[{"item":"a1","pass":false,"evidence":"no"}],"tasks":[{"task":"t1","confirmation":"confirmed"}]}`,
	)
	out, err := roles["tester"].Judge(context.Background(), failed)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestTesterJudgeListsAFailedItemWhoseRerunErroredAsUnconfirmed(t *testing.T) {
	out := judgeWithRunner(t, func(context.Context, string) (int, string, error) {
		return -1, "", errors.New("the tester has no sandbox instance")
	})

	if out.Kind != OutcomePass || len(out.Summary) != 2 {
		t.Fatalf("outcome %+v, want a pass with the item listed", out)
	}
	if !strings.Contains(out.Summary[1], "item a1 unconfirmed") || !strings.Contains(out.Summary[1], "no sandbox instance") {
		t.Fatalf("summary %q does not list the item as unconfirmed with the cause", out.Summary)
	}
}

func TestTesterJudgeListsAFailedItemWhoseRerunPassedTheOutputLimitAsUnconfirmed(t *testing.T) {
	r := newPipeRig(t, rigOpts{})
	if _, err := r.m.instance(context.Background(), "tester"); err != nil {
		t.Fatal(err)
	}

	big := fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x; exit 1", checkOutputCap+1000)
	run := r.m.Runner("tester")
	out := judgeWithRunner(t, func(ctx context.Context, _ string) (int, string, error) { return run(ctx, big) })

	if out.Kind != OutcomePass || len(out.Summary) != 2 || !strings.Contains(out.Summary[1], "item a1 unconfirmed") {
		t.Fatalf("outcome %+v, want a pass with item a1 listed as unconfirmed", out)
	}
}
