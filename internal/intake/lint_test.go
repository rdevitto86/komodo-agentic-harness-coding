package intake

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bpGroup returns a valid group named name with one valid task and the given dependencies.
func bpGroup(name string, deps ...string) Group {
	dir := "pkg-" + name
	if deps == nil {
		deps = []string{}
	}

	return Group{
		Name:         name,
		Goal:         "Group goal",
		Points:       3,
		Subsystem:    "none",
		Priority:     PriorityNormal,
		Files:        []string{dir + "/a.go"},
		Dependencies: deps,
		Tasks: []Task{{
			Name:          "tsk-one",
			Files:         []string{dir + "/a.go"},
			DoneWhen:      "go test ./...",
			EstimateLines: 100,
			Acceptance:    []Acceptance{{Keyword: "WHEN", Text: "a thing happens THE SYSTEM SHALL respond"}},
		}},
	}
}

// bpPlan returns a valid stored plan with the given groups; a group without an id gets one.
func bpPlan(groups ...Group) Blueprint {
	for i := range groups {
		if groups[i].ID == "" {
			groups[i].ID = uid(i + 1)
		}
	}

	return Blueprint{Plan: "plan-aaaaaa", Repo: "repo-1", Goal: "Plan goal", Release: "V1", Groups: groups}
}

// draftOfPlan renders a blueprint as a one-file draft.
func draftOfPlan(b Blueprint) PlanDraft {
	return PlanDraft{Files: []DraftFile{{Path: "plan.md", Content: Render(b)}}}
}

// only returns the findings of one rule and severity, failing the test unless there are n of them.
func only(t *testing.T, fs []Finding, r Rule, s Severity, n int) []Finding {
	t.Helper()

	got := rulesOf(fs, r, s)
	if len(got) != n {
		t.Fatalf("want %d %s/%s findings, got %d: %s", n, r, s, len(got), findingText(fs))
	}

	return got
}

func TestLint(t *testing.T) {
	t.Parallel()

	t.Run("the fixture plan passes and Parse then Lint equals Lint of the stored blueprint", func(t *testing.T) {
		t.Parallel()

		b, found := Parse(readPlanFixture(t))
		if len(found) != 0 {
			t.Fatalf("parse findings: %s", findingText(found))
		}

		fs := Lint(b, nil, nil)
		if len(fails(fs)) != 0 {
			t.Fatalf("findings: %s", findingText(fs))
		}

		b.Repo = "repo-1"
		home := t.TempDir()

		if err := WriteBlueprint(home, b); err != nil {
			t.Fatal(err)
		}

		stored, err := LoadBlueprint(home, "repo-1", b.Plan)
		if err != nil {
			t.Fatal(err)
		}

		if got := Lint(stored, nil, nil); !reflect.DeepEqual(got, fs) {
			t.Fatalf("stored %v, parsed %v", got, fs)
		}
	})

	t.Run("a task needs a done_when or an acceptance criterion", func(t *testing.T) {
		t.Parallel()

		cases := map[string]struct {
			doneWhen   string
			acceptance []Acceptance
			fails      bool
		}{
			"neither":         {fails: true},
			"only done_when":  {doneWhen: "go test ./..."},
			"only acceptance": {acceptance: []Acceptance{{Keyword: "IF", Text: "x THEN THE SYSTEM SHALL y"}}},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				g := bpGroup("tg-1")
				g.Tasks[0].DoneWhen, g.Tasks[0].Acceptance = c.doneWhen, c.acceptance

				got := rulesOf(Lint(bpPlan(g), nil, nil), RuleRequired, SeverityFail)
				if (len(got) == 1) != c.fails || len(got) > 1 {
					t.Fatalf("findings: %v", got)
				}

				if c.fails && got[0].Path != "plan.md#tg-1/tsk-one" {
					t.Fatalf("path %q", got[0].Path)
				}
			})
		}
	})

	t.Run("a plan needs a release", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"))
		b.Release = ""

		got := only(t, Lint(b, nil, nil), RuleRequired, SeverityFail, 1)
		if got[0].Path != "plan.md" || !strings.Contains(got[0].Message, "release") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("a stored plan needs an id", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"))
		b.Plan = ""

		got := only(t, Lint(b, nil, nil), RuleGrammar, SeverityFail, 1)
		if got[0].Path != "plan.md" || !strings.Contains(got[0].Message, "id") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("files must be listed and inside the group", func(t *testing.T) {
		t.Parallel()

		g := bpGroup("tg-1")
		g.Files = nil
		g.Tasks[0].Files = nil

		only(t, Lint(bpPlan(g), nil, nil), RuleRequired, SeverityFail, 2)

		g = bpGroup("tg-1")
		g.Tasks[0].Files = []string{"elsewhere/b.go"}

		got := only(t, Lint(bpPlan(g), nil, nil), RuleRequired, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "elsewhere/b.go") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("two groups in one wave sharing a file fail wave isolation", func(t *testing.T) {
		t.Parallel()

		a, b := bpGroup("tg-1"), bpGroup("tg-2")
		a.Files, b.Files = []string{"shared/x.go"}, []string{"shared/x.go"}
		a.Tasks[0].Files, b.Tasks[0].Files = a.Files, b.Files

		got := only(t, Lint(bpPlan(a, b), nil, nil), RuleWaveIsolation, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "shared/x.go") || got[0].Path != "plan.md#tg-2" {
			t.Fatalf("finding: %+v", got[0])
		}

		b.Dependencies = []string{"tg-1"}
		only(t, Lint(bpPlan(a, b), nil, nil), RuleWaveIsolation, SeverityFail, 0)
	})

	t.Run("a cycle fails the graph rule", func(t *testing.T) {
		t.Parallel()

		got := only(
			t,
			Lint(bpPlan(bpGroup("tg-1", "tg-2"), bpGroup("tg-2", "tg-1")), nil, nil),
			RuleGraph, SeverityFail, 1,
		)
		if !strings.Contains(got[0].Message, "cycle") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("a dependency on no group fails the graph rule", func(t *testing.T) {
		t.Parallel()

		got := only(t, Lint(bpPlan(bpGroup("tg-1", "tg-9")), nil, nil), RuleGraph, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "tg-9") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("a chain of 5 warns and does not fail; a chain of 4 is quiet", func(t *testing.T) {
		t.Parallel()

		var groups []Group
		for n := 1; n <= 5; n++ {
			if n == 1 {
				groups = append(groups, bpGroup("tg-1"))
				continue
			}

			groups = append(groups, bpGroup(fmt.Sprintf("tg-%d", n), fmt.Sprintf("tg-%d", n-1)))
		}

		fs := Lint(bpPlan(groups...), nil, nil)
		only(t, fs, RuleGraph, SeverityWarn, 1)

		if len(fails(fs)) != 0 {
			t.Fatalf("findings: %s", findingText(fs))
		}

		only(t, Lint(bpPlan(groups[:4]...), nil, nil), RuleGraph, SeverityWarn, 0)
	})

	t.Run("a group with 6 dependencies warns", func(t *testing.T) {
		t.Parallel()

		var (
			groups []Group
			deps   []string
		)
		for n := 1; n <= 6; n++ {
			groups = append(groups, bpGroup(fmt.Sprintf("tg-%d", n)))
			deps = append(deps, fmt.Sprintf("tg-%d", n))
		}

		groups = append(groups, bpGroup("tg-7", deps...))

		got := only(t, Lint(bpPlan(groups...), nil, nil), RuleGraph, SeverityWarn, 1)
		if !strings.Contains(got[0].Message, "6 dependencies") || got[0].Path != "plan.md#tg-7" {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("an acceptance item without SHALL or off the shapes fails ears", func(t *testing.T) {
		t.Parallel()

		g := bpGroup("tg-1")
		g.Tasks[0].Acceptance = []Acceptance{
			{Keyword: "WHEN", Text: "a thing happens THE SYSTEM refuses"},
			{Keyword: "WHEN", Text: "SHALL do it"},
		}

		got := only(t, Lint(bpPlan(g), nil, nil), RuleEARS, SeverityFail, 2)
		if got[0].Path != "plan.md#tg-1/tsk-one" || !strings.Contains(got[0].Message, "item 1") {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("sizing limits", func(t *testing.T) {
		t.Parallel()

		cases := map[string]func(g *Group){
			"6 files": func(g *Group) {
				g.Tasks[0].Files = []string{"a", "b", "c", "d", "e", "f"}
				g.Files = g.Tasks[0].Files
			},
			"301 lines":     func(g *Group) { g.Tasks[0].EstimateLines = 301 },
			"9 tasks":       func(g *Group) { g.Tasks = append(g.Tasks, make([]Task, 8)...) },
			"4 points":      func(g *Group) { g.Points = 4 },
			"9 points":      func(g *Group) { g.Points = 9 },
			"zero points":   func(g *Group) { g.Points = 0 },
			"exactly 5 ok":  nil,
			"exactly 300ok": func(g *Group) { g.Tasks[0].EstimateLines = 300 },
		}
		for name, mod := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				g := bpGroup("tg-1")
				if mod != nil {
					mod(&g)
				}

				want := 1
				if mod == nil || strings.HasSuffix(name, "ok") {
					want = 0
				}

				only(t, Lint(bpPlan(g), nil, nil), RuleSizing, SeverityFail, want)
			})
		}
	})

	t.Run("a protected file in a group fails", func(t *testing.T) {
		t.Parallel()

		g := bpGroup("tg-1")
		g.Files = append(g.Files, ".github/ci.yml")

		got := only(t, Lint(bpPlan(g), []string{".github/**"}, nil), RuleProtected, SeverityFail, 1)
		if !strings.Contains(got[0].Message, ".github/ci.yml") {
			t.Fatalf("finding: %+v", got[0])
		}

		only(t, Lint(bpPlan(g), nil, nil), RuleProtected, SeverityFail, 0)
	})

	t.Run("paths that differ only by case fail", func(t *testing.T) {
		t.Parallel()

		g := bpGroup("tg-1")
		g.Files = []string{"Readme.md", "pkg-tg-1/a.go"}

		got := only(t, Lint(bpPlan(g), nil, []string{"README.md"}), RuleCaseCollision, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "README.md") || got[0].Path != "plan.md#tg-1" {
			t.Fatalf("finding: %+v", got[0])
		}

		g.Tasks = append(g.Tasks, Task{Name: "tsk-ONE", Files: g.Files[:1], DoneWhen: "x", EstimateLines: 1})
		got = only(t, Lint(bpPlan(g), nil, nil), RuleCaseCollision, SeverityFail, 1)

		if got[0].Path != "plan.md#tg-1/tsk-ONE" {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("two groups with the same id fail grammar", func(t *testing.T) {
		t.Parallel()

		a, b := bpGroup("tg-1"), bpGroup("tg-2", "tg-1")
		a.ID, b.ID = uid(7), uid(7)

		got := only(t, Lint(bpPlan(a, b), nil, nil), RuleGrammar, SeverityFail, 1)
		if got[0].Path != "plan.md#tg-2" {
			t.Fatalf("finding: %+v", got[0])
		}
	})

	t.Run("findings come back sorted by path and then rule", func(t *testing.T) {
		t.Parallel()

		g := bpGroup("tg-1")
		g.Points = 4
		g.Tasks[0].Acceptance = []Acceptance{{Keyword: "WHEN", Text: "no shall"}}

		b := bpPlan(g)
		b.Release = ""

		fs := Lint(b, nil, nil)
		if len(fs) < 3 {
			t.Fatalf("findings: %s", findingText(fs))
		}

		for i := 1; i < len(fs); i++ {
			a, c := fs[i-1], fs[i]
			if a.Path > c.Path || (a.Path == c.Path && a.Rule > c.Rule) {
				t.Fatalf("not sorted: %s", findingText(fs))
			}
		}
	})
}

func TestLintDraft(t *testing.T) {
	t.Parallel()

	t.Run("a draft may leave the plan id out while Lint of a stored plan may not", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"))
		b.Plan = ""

		if fs := LintDraft(draftOfPlan(b), Blueprint{}, nil, nil); len(fs) != 0 {
			t.Fatalf("draft findings: %s", findingText(fs))
		}

		only(t, Lint(b, nil, nil), RuleGrammar, SeverityFail, 1)
	})

	t.Run("a draft of a new plan with an id fails; an amending draft carries the stored id", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"))
		only(t, LintDraft(draftOfPlan(b), Blueprint{}, nil, nil), RuleGrammar, SeverityFail, 1)

		stored := bpPlan(bpGroup("tg-1"))
		if fs := LintDraft(draftOfPlan(b), stored, nil, nil); len(fs) != 0 {
			t.Fatalf("same id: %s", findingText(fs))
		}

		b.Plan = "plan-bbbbbb"
		only(t, LintDraft(draftOfPlan(b), stored, nil, nil), RuleGrammar, SeverityFail, 1)
	})

	t.Run("a draft with no plan.md fails grammar", func(t *testing.T) {
		t.Parallel()

		only(t, LintDraft(PlanDraft{}, Blueprint{}, nil, nil), RuleGrammar, SeverityFail, 1)
		only(
			t,
			LintDraft(PlanDraft{Files: []DraftFile{{Path: "other.md", Content: "x"}}}, Blueprint{}, nil, nil),
			RuleGrammar, SeverityFail, 1,
		)
	})

	t.Run("a task missing both checks yields one required finding, not two", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"))
		b.Plan = ""
		b.Groups[0].Tasks[0].DoneWhen, b.Groups[0].Tasks[0].Acceptance = "", nil

		only(t, LintDraft(draftOfPlan(b), Blueprint{}, nil, nil), RuleRequired, SeverityFail, 1)
	})

	t.Run("a missing release fails a draft", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"))
		b.Plan, b.Release = "", ""

		only(t, LintDraft(draftOfPlan(b), Blueprint{}, nil, nil), RuleRequired, SeverityFail, 1)
	})

	t.Run("a new group takes the next number", func(t *testing.T) {
		t.Parallel()

		stored := bpPlan(bpGroup("tg-1"))
		next := bpPlan(bpGroup("tg-1"), bpGroup("tg-2"))
		skip := bpPlan(bpGroup("tg-1"), bpGroup("tg-3"))
		fresh := bpPlan(bpGroup("tg-2"))

		if fs := LintDraft(draftOfPlan(next), stored, nil, nil); len(fs) != 0 {
			t.Fatalf("next number: %s", findingText(fs))
		}

		got := only(t, LintDraft(draftOfPlan(skip), stored, nil, nil), RuleGrammar, SeverityFail, 1)
		if !strings.Contains(got[0].Message, "tg-2") || got[0].Path != "plan.md#tg-3" {
			t.Fatalf("finding: %+v", got[0])
		}

		fresh.Plan = ""
		only(t, LintDraft(draftOfPlan(fresh), Blueprint{}, nil, nil), RuleGrammar, SeverityFail, 1)
	})

	t.Run("a duplicate or zero-padded group number fails grammar", func(t *testing.T) {
		t.Parallel()

		for name, text := range map[string]string{
			"duplicate": "# Plan\ngoal: g\nrelease: V1\n\n## tg-1\n\n## tg-1\n",
			"leading 0": "# Plan\ngoal: g\nrelease: V1\n\n## tg-01\n",
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				d := PlanDraft{Files: []DraftFile{{Path: "plan.md", Content: text}}}
				if len(rulesOf(LintDraft(d, Blueprint{}, nil, nil), RuleGrammar, SeverityFail)) == 0 {
					t.Fatal("want a grammar finding")
				}
			})
		}
	})

	t.Run("a flaw in any group fails the draft", func(t *testing.T) {
		t.Parallel()

		b := bpPlan(bpGroup("tg-1"), bpGroup("tg-2", "tg-1"))
		b.Plan = ""
		b.Groups[1].Points = 4

		only(t, LintDraft(draftOfPlan(b), Blueprint{}, nil, nil), RuleSizing, SeverityFail, 1)
	})
}

func TestApprove(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T, planText string) (home string) {
		t.Helper()

		home = t.TempDir()
		dir := PlanDir(home, "repo-1", "plan-7f3a9c")

		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(planText), 0o644); err != nil {
			t.Fatal(err)
		}

		return home
	}

	t.Run("a good plan is written to blueprint.json with its card hashes", func(t *testing.T) {
		t.Parallel()

		text := readPlanFixture(t)
		home := setup(t, text)

		b, fs, err := Approve(home, "repo-1", "plan-7f3a9c", nil, nil)
		if err != nil || len(fails(fs)) != 0 {
			t.Fatalf("err %v findings %s", err, findingText(fs))
		}

		if b.Repo != "repo-1" || b.Plan != "plan-7f3a9c" || len(b.Groups) != 2 {
			t.Fatalf("blueprint: %+v", b)
		}

		for _, g := range b.Groups {
			if want, _ := CardHash(text, g); g.Hash == "" || g.Hash != want {
				t.Fatalf("group %s hash %q, want %q", g.Name, g.Hash, want)
			}
		}

		got, err := LoadBlueprint(home, "repo-1", "plan-7f3a9c")
		if err != nil || !reflect.DeepEqual(got, b) {
			t.Fatalf("stored %+v, err %v", got, err)
		}
	})

	t.Run("a plan edited into a broken state is refused and the blueprint stays", func(t *testing.T) {
		t.Parallel()

		good := readPlanFixture(t)
		home := setup(t, good)

		if _, _, err := Approve(home, "repo-1", "plan-7f3a9c", nil, nil); err != nil {
			t.Fatal(err)
		}

		before, err := os.ReadFile(filepath.Join(PlanDir(home, "repo-1", "plan-7f3a9c"), "blueprint.json"))
		if err != nil {
			t.Fatal(err)
		}

		broken := strings.Replace(good, "points: 3", "points: 4", 1)
		broken = strings.Replace(broken, "release: V1\n", "", 1)

		planPath := filepath.Join(PlanDir(home, "repo-1", "plan-7f3a9c"), "plan.md")
		if err := os.WriteFile(planPath, []byte(broken), 0o644); err != nil {
			t.Fatal(err)
		}

		b, fs, err := Approve(home, "repo-1", "plan-7f3a9c", nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		only(t, fs, RuleSizing, SeverityFail, 1)
		only(t, fs, RuleRequired, SeverityFail, 1)

		if !reflect.DeepEqual(b, Blueprint{}) {
			t.Fatalf("a refused plan returned %+v", b)
		}

		after, err := os.ReadFile(filepath.Join(PlanDir(home, "repo-1", "plan-7f3a9c"), "blueprint.json"))
		if err != nil || string(after) != string(before) {
			t.Fatalf("blueprint changed: %v", err)
		}
	})

	t.Run("a plan.md id that is not the folder name is refused", func(t *testing.T) {
		t.Parallel()

		home := setup(t, strings.Replace(readPlanFixture(t), "plan-7f3a9c", "plan-000000", 1))

		_, fs, err := Approve(home, "repo-1", "plan-7f3a9c", nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		only(t, fs, RuleGrammar, SeverityFail, 1)

		if _, err := os.Stat(filepath.Join(PlanDir(home, "repo-1", "plan-7f3a9c"), "blueprint.json")); err == nil {
			t.Fatal("a blueprint was written")
		}
	})

	t.Run("a plan without an id is refused", func(t *testing.T) {
		t.Parallel()

		home := setup(t, strings.Replace(readPlanFixture(t), "id: plan-7f3a9c\n", "", 1))

		_, fs, err := Approve(home, "repo-1", "plan-7f3a9c", nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		only(t, fs, RuleGrammar, SeverityFail, 1)
	})

	t.Run("an unreadable or badly named plan is an error", func(t *testing.T) {
		t.Parallel()

		if _, _, err := Approve(t.TempDir(), "repo-1", "plan-000000", nil, nil); err == nil {
			t.Fatal("a missing plan.md must be an error")
		}

		if _, _, err := Approve(t.TempDir(), "repo-1", "../x", nil, nil); err == nil {
			t.Fatal("a plan id with a separator must be an error")
		}
	})

	t.Run("a blueprint that cannot be written is an error", func(t *testing.T) {
		t.Parallel()

		home := setup(t, readPlanFixture(t))
		blockers := filepath.Join(PlanDir(home, "repo-1", "plan-7f3a9c"), "blueprint.json")

		if err := os.Mkdir(blockers, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(blockers, "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		if _, _, err := Approve(home, "repo-1", "plan-7f3a9c", nil, nil); err == nil {
			t.Fatal("want a write error")
		}
	})
}

func TestOverEstimate(t *testing.T) {
	t.Parallel()

	tk := Task{Name: "tsk-a", EstimateLines: 100}

	if f, ok := OverEstimate(tk, 210); !ok || f.Rule != RuleSizing || f.Severity != SeverityWarn {
		t.Fatalf("2.1 times: %+v %v", f, ok)
	}

	if _, ok := OverEstimate(tk, 200); ok {
		t.Fatal("exactly 2 times must not warn")
	}
}
