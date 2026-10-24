package task

import (
	"context"
	"slices"
	"testing"
)

func TestCriteriaOnlyTaskFinish(t *testing.T) {
	cases := []struct {
		name     string
		criteria []Criterion
		wantPass bool
	}{
		{"two passing cmd lines finish the task", []Criterion{{Text: "a", Cmd: "ok1"}, {Text: "b", Cmd: "ok2"}}, true},
		{"one failing cmd line blocks the task", []Criterion{{Text: "a", Cmd: "ok1"}, {Text: "b", Cmd: "bad"}}, false},
		{"a criterion with no cmd is left to Quality Assurance", []Criterion{{Text: "a"}, {Text: "b", Cmd: "ok1"}}, true},
		{"only cmd-less criteria pass", []Criterion{{Text: "a"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) {
				s.Tasks[0].DoneWhen = ""
				s.Tasks[0].Criteria = tc.criteria
			}})
			r.m.c.Commands["ok1"] = []string{"true"}
			r.m.c.Commands["ok2"] = []string{"true"}
			r.m.c.Commands["bad"] = []string{"sh", "-c", "echo broke; exit 3"}
			if _, err := r.m.instance(context.Background(), "builder"); err != nil {
				t.Fatal(err)
			}

			exit, out, err := r.m.finishTask(context.Background(), "t1")
			if err != nil || (exit == 0) != tc.wantPass {
				t.Fatalf("finishTask: %d %q, %v", exit, out, err)
			}
			if got := slices.Contains(r.m.Progress().TasksDone, "t1"); got != tc.wantPass {
				t.Fatalf("done %v", r.m.Progress().TasksDone)
			}
			if want := map[bool]int{true: 1, false: 0}[tc.wantPass]; r.cm.saveCount() != want {
				t.Fatalf("saves %d, want %d", r.cm.saveCount(), want)
			}
			if !tc.wantPass && exit != 3 {
				t.Fatalf("exit %d, want the failing command's 3", exit)
			}
		})
	}
}

func TestTaskWithNeitherDoneWhenNorCriteriaIsAnError(t *testing.T) {
	r := newPipeRig(t, rigOpts{mut: func(s *GroupSpec, _ *Collaborators) { s.Tasks[0].DoneWhen = "" }})
	if _, err := r.m.instance(context.Background(), "builder"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.m.finishTask(context.Background(), "t1"); err == nil {
		t.Fatal("a task with no command and no criteria finished")
	}
}
