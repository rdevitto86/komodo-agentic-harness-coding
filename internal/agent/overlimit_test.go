package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// overLimit is the error a runner returns for a command whose output passed its limit.
func overLimit() error {
	return fmt.Errorf("%w (16777216 bytes); full output in /x.log", ErrOutputOverLimit)
}

func TestTaskGateUsesTheExitCodeOfACommandWhoseOutputPassedTheLimit(t *testing.T) {
	ref := TaskRef{Group: "tg-1", Task: "tsk-a", DoneCommand: "go test ./..."}
	for _, exit := range []int{0, 3} {
		run := func(context.Context, string) (int, string, error) { return exit, "head\ntail", overLimit() }

		o, err := TaskGate{Run: run}.Complete(context.Background(), ref)
		if err != nil || o.ExitCode != exit || o.Output != "head\ntail" {
			t.Fatalf("exit %d: %+v, %v", exit, o, err)
		}
	}

	other := func(context.Context, string) (int, string, error) { return -1, "", errors.New("no instance") }
	if _, err := (TaskGate{Run: other}).Complete(context.Background(), ref); err == nil {
		t.Error("another runner error passed")
	}
}

func TestRerunUsesTheExitCodeOfACommandWhoseOutputPassedTheLimit(t *testing.T) {
	it := Item{ID: "a1", Cmd: "go test ./..."}
	for exit, want := range map[int]RerunOutcome{1: RerunConfirmed, 0: RerunDisputed} {
		run := func(context.Context, string) (int, string, error) { return exit, "head\ntail", overLimit() }

		got, out, err := goTester{}.Rerun(context.Background(), run, it)
		if err != nil || got != want || out != "head\ntail" {
			t.Fatalf("exit %d: %q, %q, %v, want %q", exit, got, out, err, want)
		}
	}

	other := func(context.Context, string) (int, string, error) { return -1, "", errors.New("no instance") }
	if _, _, err := (goTester{}).Rerun(context.Background(), other, it); err == nil {
		t.Error("another runner error passed")
	}
}

func TestDoneWhenStepUsesTheExitCodeOfACommandWhoseOutputPassedTheLimit(t *testing.T) {
	in := CheckInput{DoneWhen: map[string]string{"tsk-a": "go test ./a/..."}}
	for exit, want := range map[int]Level{0: LevelPass, 3: LevelFail} {
		run := func(context.Context, string) (int, string, error) { return exit, "head\ntail", overLimit() }

		s, err := doneWhenStep(context.Background(), run, in)
		if err != nil || s.Level != want {
			t.Fatalf("exit %d: %+v, %v, want level %v", exit, s, err, want)
		}
	}

	other := func(context.Context, string) (int, string, error) { return -1, "", errors.New("no instance") }
	if _, err := doneWhenStep(context.Background(), other, in); err == nil || strings.Contains(err.Error(), "limit") {
		t.Errorf("another runner error: %v", err)
	}
}
