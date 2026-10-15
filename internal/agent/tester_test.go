package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestTesterRole(t *testing.T) {
	r := TesterRole()
	if r.Name != "tester" || !r.PoolSlot || !slices.Equal(r.Tools, []string{"read", "search", "run"}) {
		t.Fatalf("role = %+v", r)
	}
	if !slices.Equal(r.RunClasses, []string{"build", "test"}) || !slices.Equal(r.Guard, []string{"R1"}) {
		t.Errorf("classes %v guard %v", r.RunClasses, r.Guard)
	}
	if !slices.Contains(r.Slots.Required, "acceptance") || !slices.Contains(r.Slots.Optional, "baseline") {
		t.Errorf("slots = %+v", r.Slots)
	}
}

func TestAdjudicateItem(t *testing.T) {
	cases := []struct {
		in   [2]bool
		want RerunOutcome
	}{
		{[2]bool{false, false}, RerunConfirmed}, {[2]bool{true, true}, RerunDisputed},
		{[2]bool{true, false}, RerunSplit}, {[2]bool{false, true}, RerunSplit},
	}
	for _, c := range cases {
		if got := AdjudicateItem(c.in); got != c.want {
			t.Errorf("AdjudicateItem(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestClassifyFailure(t *testing.T) {
	if ClassifyFailure([2]bool{false, false}) != FlakeConfirmed {
		t.Error("two failing reruns are not confirmed")
	}
	for _, in := range [][2]bool{{true, false}, {false, true}, {true, true}} {
		if ClassifyFailure(in) != FlakeFlaky {
			t.Errorf("ClassifyFailure(%v) is not flaky", in)
		}
	}
}

// scripted returns a runner that answers each call from a list of exit codes and records the commands.
func scripted(exits []int, ran *[]string) CommandRunner {
	return func(_ context.Context, c string) (int, string, error) {
		i := len(*ran)
		*ran = append(*ran, c)
		return exits[i], "out" + string(rune('0'+i)), nil
	}
}

func TestTesterRerun(t *testing.T) {
	cases := []struct {
		name  string
		exits []int
		want  RerunOutcome
	}{
		{"both fail", []int{1, 2}, RerunConfirmed}, {"both pass", []int{0, 0}, RerunDisputed},
		{"split", []int{0, 1}, RerunSplit},
	}
	for _, c := range cases {
		var ran []string
		got, out, err := NewTester().Rerun(context.Background(), scripted(c.exits, &ran), Item{ID: "ac-1", Cmd: "go test ./x"})
		if err != nil || got != c.want || len(ran) != FlakeReruns || ran[0] != "go test ./x" || out != "out1" {
			t.Errorf("%s: %s %q %v ran %v", c.name, got, out, err, ran)
		}
	}
}

func TestTesterRerunNoCommandAndRunnerError(t *testing.T) {
	var ran []string
	got, _, err := NewTester().Rerun(context.Background(), scripted(nil, &ran), Item{ID: "ac-2"})
	if err != nil || got != RerunUnconfirmed || len(ran) != 0 {
		t.Fatalf("no command: %s %v ran %v", got, err, ran)
	}
	boom := errors.New("cannot start")
	_, _, err = NewTester().Rerun(context.Background(), func(context.Context, string) (int, string, error) { return 0, "", boom }, Item{ID: "ac-3", Cmd: "x"})
	if !errors.Is(err, boom) {
		t.Fatalf("runner error = %v", err)
	}
}

func TestTesterAcceptResult(t *testing.T) {
	tasks := []string{"tsk-a", "tsk-b"}
	good := TesterResult{
		Items: []ItemResult{{Item: "ac-1", Pass: true, Evidence: "ok"}},
		Tasks: []TaskConfirmation{{Task: "tsk-a", Confirmation: Confirmed}, {Task: "tsk-b", Confirmation: NotConfirmed}},
	}
	tr := NewTester()
	if err := tr.AcceptResult(tasks, []string{"ac-1"}, good); err != nil {
		t.Fatalf("good result: %v", err)
	}
	mut := func(f func(r *TesterResult)) TesterResult {
		r := TesterResult{Items: slices.Clone(good.Items), Tasks: slices.Clone(good.Tasks)}
		f(&r)
		return r
	}
	bad := map[string]TesterResult{
		"repeated task": mut(func(r *TesterResult) { r.Tasks[1].Task = "tsk-a" }),
		"skipped task":  mut(func(r *TesterResult) { r.Tasks = r.Tasks[:1] }),
		"extra task": mut(func(r *TesterResult) {
			r.Tasks = append(r.Tasks, TaskConfirmation{Task: "tsk-z", Confirmation: Confirmed})
		}),
		"empty evidence": mut(func(r *TesterResult) { r.Items[0].Evidence = "  " }),
		"bad word":       mut(func(r *TesterResult) { r.Tasks[0].Confirmation = "yes" }),
		"bad reason":     mut(func(r *TesterResult) { r.Items[0].Reason = "because" }),
	}
	for name, r := range bad {
		if err := tr.AcceptResult(tasks, []string{"ac-1"}, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseTesterResult(t *testing.T) {
	raw := `{"items": [{"item": "ac-1", "pass": true, "evidence": "ok"}], "tasks": [{"task": "t", "confirmation": "confirmed"}]}`
	if r, err := ParseTesterResult([]byte(raw)); err != nil || len(r.Items) != 1 || r.Tasks[0].Confirmation != Confirmed {
		t.Fatalf("parse: %+v %v", r, err)
	}
	if _, err := ParseTesterResult([]byte(`{"items": [], "tasks": [], "x": 1}`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestBaselineClassify(t *testing.T) {
	b := Baseline{TreeHash: "abc", Passed: map[string]bool{"TestOK": true, "TestOld": false}}
	cases := []struct {
		test string
		head bool
		want TestStatus
	}{
		{"TestOK", true, StatusOK}, {"TestOK", false, StatusRegression}, {"TestOld", false, StatusPreExisting},
		{"TestOld", true, StatusOK}, {"TestNew", false, StatusNew}, {"TestNew", true, StatusOK},
	}
	for _, c := range cases {
		if got := b.Classify(c.test, c.head); got != c.want {
			t.Errorf("Classify(%s, %v) = %s, want %s", c.test, c.head, got, c.want)
		}
	}
}

func TestImpactSet(t *testing.T) {
	deps := map[string][]string{"a": nil, "b": {"a"}, "c": {"b", "a"}, "d": nil}
	if got := ImpactSet(deps, []string{"a"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("impact of a = %v", got)
	}
	if got := ImpactSet(deps, []string{"b"}); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("impact of b = %v", got)
	}
	if got := ImpactSet(deps, nil); len(got) != 0 {
		t.Errorf("no change = %v", got)
	}
}

func TestCaseCollisions(t *testing.T) {
	got := CaseCollisions([]string{"README.md", "src/a.go", "readme.md", "src/A.go", "other.txt"})
	if len(got) != 2 {
		t.Fatalf("collisions = %v", got)
	}
	if !slices.ContainsFunc(got, func(g []string) bool { return reflect.DeepEqual(g, []string{"README.md", "readme.md"}) }) {
		t.Errorf("README group missing: %v", got)
	}
	if len(CaseCollisions([]string{"a", "b"})) != 0 {
		t.Error("distinct paths collided")
	}
}

func TestCheckResultClean(t *testing.T) {
	warn := CheckResult{Steps: []StepResult{{Step: StepScope, Level: LevelPass}, {Step: StepFlake, Level: LevelWarn}}}
	if !warn.Clean() {
		t.Error("warnings made the result unclean")
	}
	fail := CheckResult{Steps: append(slices.Clone(warn.Steps), StepResult{Step: StepBaseline, Level: LevelFail})}
	if fail.Clean() {
		t.Error("a failing step left the result clean")
	}
	d := fail.Done("deadbeef")
	if d.Pass || !d.Regression || d.Head != "deadbeef" {
		t.Errorf("done = %+v", d)
	}
	if d := warn.Done("h"); !d.Pass || d.Regression {
		t.Errorf("clean done = %+v", d)
	}
}
