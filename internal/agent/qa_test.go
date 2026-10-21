package agent

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestQARole(t *testing.T) {
	r := QARole()
	if r.Name != "qa" || !r.PoolSlot || !slices.Equal(r.Tools, []string{"read", "search", "run", "run_check"}) {
		t.Fatalf("role = %+v", r)
	}
	if !slices.Equal(r.RunClasses, []string{"build", "test"}) || !slices.Equal(r.Guard, []string{"R1"}) {
		t.Errorf("classes %v guard %v", r.RunClasses, r.Guard)
	}
	if !slices.Contains(r.Slots.Required, "acceptance") || !slices.Contains(r.Slots.Optional, "baseline") {
		t.Errorf("slots = %+v", r.Slots)
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

func intp(n int) *int { return &n }

func TestQAAcceptResult(t *testing.T) {
	tasks := []string{"tsk-a", "tsk-b"}
	good := QAResult{
		Status: QATested,
		Items: []ItemResult{{
			Item: "ac-1", Pass: true, Confidence: intp(90),
			Evidence: []EvidenceRun{{Cmd: "go test", Excerpt: "ok"}},
		}},
		Tasks: []TaskConfirmation{{Task: "tsk-a", Confirmation: Confirmed}, {Task: "tsk-b", Confirmation: NotConfirmed}},
	}
	tr := NewQA()
	if err := tr.AcceptResult(tasks, []string{"ac-1"}, good); err != nil {
		t.Fatalf("good result: %v", err)
	}
	mut := func(f func(r *QAResult)) QAResult {
		r := QAResult{Status: QATested, Items: slices.Clone(good.Items), Tasks: slices.Clone(good.Tasks)}
		f(&r)
		return r
	}
	bad := map[string]QAResult{
		"repeated task": mut(func(r *QAResult) { r.Tasks[1].Task = "tsk-a" }),
		"skipped task":  mut(func(r *QAResult) { r.Tasks = r.Tasks[:1] }),
		"extra task": mut(func(r *QAResult) {
			r.Tasks = append(r.Tasks, TaskConfirmation{Task: "tsk-z", Confirmation: Confirmed})
		}),
		"empty evidence": mut(func(r *QAResult) { r.Items[0].Evidence = nil }),
		"blank command": mut(func(r *QAResult) {
			r.Items[0].Evidence = []EvidenceRun{{Cmd: "  ", Excerpt: "ok"}}
		}),
		"excerpt over 4 KB": mut(func(r *QAResult) {
			r.Items[0].Evidence = []EvidenceRun{{Cmd: "go test", Excerpt: strings.Repeat("x", GateOutputCap+1)}}
		}),
		"bad word":          mut(func(r *QAResult) { r.Tasks[0].Confirmation = "yes" }),
		"no status":         mut(func(r *QAResult) { r.Status = "" }),
		"untestable no why": mut(func(r *QAResult) { r.Status, r.Cause = QAUntestable, " " }),
		"no confidence":     mut(func(r *QAResult) { r.Items[0].Confidence = nil }),
		"confidence over":   mut(func(r *QAResult) { r.Items[0].Confidence = intp(101) }),
		"confidence negate": mut(func(r *QAResult) { r.Items[0].Confidence = intp(-1) }),
	}
	for name, r := range bad {
		if err := tr.AcceptResult(tasks, []string{"ac-1"}, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	untestable := QAResult{Status: QAUntestable, Cause: "docker did not start after 2 attempts"}
	if err := tr.AcceptResult(tasks, []string{"ac-1"}, untestable); err != nil {
		t.Errorf("untestable result with a cause: %v", err)
	}

	for _, n := range []int{0, 100} {
		r := mut(func(r *QAResult) { r.Items[0].Confidence = intp(n) })
		if err := tr.AcceptResult(tasks, []string{"ac-1"}, r); err != nil {
			t.Errorf("confidence %d refused: %v", n, err)
		}
	}
}

func TestParseQAResult(t *testing.T) {
	raw := `{"status": "tested", "items": [{"item": "ac-1", "pass": true, "confidence": 85, ` +
		`"evidence": [{"cmd": "go test", "exit": 0, "excerpt": "ok"}]}], ` +
		`"tasks": [{"task": "t", "confirmation": ` +
		`"confirmed"}]}`
	r, err := ParseQAResult([]byte(raw))
	if err != nil || len(r.Items) != 1 || r.Tasks[0].Confirmation != Confirmed {
		t.Fatalf("parse: %+v %v", r, err)
	}
	if r.Items[0].Confidence == nil || *r.Items[0].Confidence != 85 {
		t.Errorf("confidence = %v", r.Items[0].Confidence)
	}
	if _, err := ParseQAResult([]byte(`{"items": [], "tasks": [], "x": 1}`)); err == nil {
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
