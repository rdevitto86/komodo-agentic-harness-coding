package task

import (
	"context"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// passRole builds the QA role over one item a1 (command "test") whose runs go through run.
func passRole(t *testing.T, run agent.CommandRunner) *qaRole {
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

	return roles["qa"].(*qaRole)
}

// passJSON is a result that passes a1 with one evidence run.
func passJSON(cmd string, exit int, excerpt string) string {
	return `{"items":[{"item":"a1","pass":true,"evidence":[{"cmd":"` + cmd + `","exit":` + string(rune('0'+exit)) +
		`,"excerpt":"` + excerpt + `"}]}],"tasks":[{"task":"t1","confirmation":"confirmed"}]}`
}

// A pass whose evidence Go replays and reproduces is a pass.
func TestQAJudgeAcceptsAReplayedPass(t *testing.T) {
	r := passRole(t, func(context.Context, string) (int, string, error) { return 0, "ok pkg\n", nil })

	out, err := r.Judge(context.Background(), result(passJSON("test", 0, "ok pkg")))
	if err != nil || out.Kind != OutcomePass {
		t.Fatalf("outcome %+v err %v", out, err)
	}
}

// A pass whose replay gives another exit code restarts Quality Assurance with Go's output.
func TestQAJudgeRestartsAnUnreplayedPassWithGoOutput(t *testing.T) {
	r := passRole(t, func(context.Context, string) (int, string, error) { return 1, "FAIL pkg\n", nil })

	out, err := r.Judge(context.Background(), result(passJSON("test", 0, "ok pkg")))
	if err != nil || out.Kind != OutcomeRestart || len(out.Findings) != 1 {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if !strings.Contains(out.Findings[0], "a1") || !strings.Contains(out.Findings[0], "FAIL pkg") ||
		!strings.Contains(out.Findings[0], "exit 1") {
		t.Errorf("finding lacks Go's output: %q", out.Findings[0])
	}

	slots, err := r.Slots(context.Background(), RoleInput{})
	if err != nil || !strings.Contains(slots["checks"], "FAIL pkg") {
		t.Errorf("restart brief checks = %q err %v", slots["checks"], err)
	}
}

// A command the model made up is neither run nor accepted; one it ran with the run tool is replayed.
func TestQAJudgeReplaysOnlyTheItemsCommandOrALoggedOne(t *testing.T) {
	var ran []string
	r := passRole(t, func(_ context.Context, c string) (int, string, error) { ran = append(ran, c); return 0, "ok\n", nil })

	out, err := r.Judge(context.Background(), result(passJSON("rm -rf x", 0, "ok")))
	if err != nil || out.Kind != OutcomeRestart || len(ran) != 0 {
		t.Fatalf("made-up command: outcome %+v err %v ran %v", out, err, ran)
	}

	r.Ran("go vet ./...")

	out, err = r.Judge(context.Background(), result(passJSON("go vet ./...", 0, "ok")))
	if err != nil || out.Kind != OutcomePass || len(ran) != 1 || ran[0] != "go vet ./..." {
		t.Fatalf("logged command: outcome %+v err %v ran %v", out, err, ran)
	}
}

// The log of run commands starts empty for each session.
func TestQARoleForgetsLoggedCommandsPerSession(t *testing.T) {
	r := passRole(t, func(context.Context, string) (int, string, error) { return 0, "ok\n", nil })
	r.Ran("go vet ./...")

	if _, err := r.Slots(context.Background(), RoleInput{}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Judge(context.Background(), result(passJSON("go vet ./...", 0, "ok")))
	if err != nil || out.Kind != OutcomeRestart {
		t.Fatalf("outcome %+v err %v", out, err)
	}
}
