package intake

import (
	"fmt"
	"strings"
	"testing"
)

func TestLintPassesAGoodEpic(t *testing.T) {
	files := cat(goodGroup(1, 1, tgSpec{}), goodGroup(1, 2, tgSpec{deps: []string{"tg-1.1"}}))
	if fs := lintFiles(t, files, []string{".github/**"}, []string{"README.md"}); len(fails(fs)) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintDraftEqualsLintOnStoredFiles(t *testing.T) {
	files := cat(goodGroup(1, 1, tgSpec{}), goodGroup(1, 2, tgSpec{items: []string{"WHEN x THE SYSTEM refuses"}}))
	stored := lintFiles(t, files, nil, nil)
	draft := LintDraft(draftOf(files), nil, nil, nil)
	if len(stored) == 0 || fmt.Sprint(stored) != fmt.Sprint(draft) {
		t.Fatalf("stored %+v\ndraft %+v", stored, draft)
	}
}

func TestLintReturnsEveryFindingSorted(t *testing.T) {
	bad := goodGroup(1, 1, tgSpec{points: "4", items: []string{"the system should work"}})
	fs := LintDraft(draftOf(bad), nil, nil, nil)
	if len(fails(fs)) < 2 {
		t.Fatalf("want sizing and ears: %+v", fs)
	}
	for i := 1; i < len(fs); i++ {
		a, b := fs[i-1], fs[i]
		if a.Path > b.Path || (a.Path == b.Path && a.Rule > b.Rule) {
			t.Fatalf("not sorted: %+v", fs)
		}
	}
}

func TestLintWaveIsolation(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{files: []string{"shared/x.go"}})
	b := goodGroup(1, 2, tgSpec{files: []string{"shared/x.go"}})
	fs := rulesOf(lintFiles(t, cat(a, b), nil, nil), RuleWaveIsolation, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "shared/x.go") || !strings.Contains(fs[0].Message, "tg-1.1") {
		t.Fatalf("findings: %+v", fs)
	}
	// Ordered groups sit in different waves and may share a file.
	b = goodGroup(1, 2, tgSpec{files: []string{"shared/x.go"}, deps: []string{"tg-1.1"}})
	if fs := rulesOf(lintFiles(t, cat(a, b), nil, nil), RuleWaveIsolation, SeverityFail); len(fs) != 0 {
		t.Fatalf("ordered groups: %+v", fs)
	}
}

func TestLintCycleFailsGraph(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{deps: []string{"tg-1.2"}})
	b := goodGroup(1, 2, tgSpec{deps: []string{"tg-1.1"}})
	fs := rulesOf(lintFiles(t, cat(a, b), nil, nil), RuleGraph, SeverityFail)
	if len(fs) == 0 || !strings.Contains(fs[0].Message, "cycle") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintUnknownDependencyFailsGraph(t *testing.T) {
	fs := rulesOf(lintFiles(t, goodGroup(1, 1, tgSpec{deps: []string{"tg-9.9"}}), nil, nil), RuleGraph, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "tg-9.9") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintLongChainWarns(t *testing.T) {
	var files []DraftFile
	for n := 1; n <= 5; n++ {
		spec := tgSpec{}
		if n > 1 {
			spec.deps = []string{fmt.Sprintf("tg-1.%d", n-1)}
		}
		files = append(files, goodGroup(1, n, spec)...)
	}
	fs := lintFiles(t, files, nil, nil)
	if len(rulesOf(fs, RuleGraph, SeverityWarn)) != 1 || len(fails(fs)) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
	// A chain of 4 does not warn.
	if fs := lintFiles(t, files[:8], nil, nil); len(rulesOf(fs, RuleGraph, SeverityWarn)) != 0 {
		t.Fatalf("chain of 4: %+v", fs)
	}
}

func TestLintManyDependenciesWarns(t *testing.T) {
	var files []DraftFile
	var deps []string
	for n := 1; n <= 6; n++ {
		files = append(files, goodGroup(1, n, tgSpec{})...)
		deps = append(deps, fmt.Sprintf("tg-1.%d", n))
	}
	files = append(files, goodGroup(1, 7, tgSpec{deps: deps})...)
	fs := rulesOf(lintFiles(t, files, nil, nil), RuleGraph, SeverityWarn)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "6 dependencies") || fs[0].Path != "epic-1/tg-1.7/TG.md" {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintGroupEveryoneWaitsOnWarns(t *testing.T) {
	files := cat(
		goodGroup(1, 1, tgSpec{}),
		goodGroup(1, 2, tgSpec{deps: []string{"tg-1.1"}}),
		goodGroup(1, 3, tgSpec{deps: []string{"tg-1.1"}}),
	)
	fs := rulesOf(lintFiles(t, files, nil, nil), RuleGraph, SeverityWarn)
	if len(fs) != 1 || fs[0].Path != "epic-1/tg-1.1/TG.md" {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintEARS(t *testing.T) {
	cases := map[string]bool{
		"WHEN a thing happens THE SYSTEM SHALL respond":  true,
		"IF the disk is full THEN THE SYSTEM SHALL stop": true,
		"WHILE paused THE SYSTEM SHALL queue events":     true,
		"WHEN a thing happens THE SYSTEM responds":       false,
		"WHEN a thing happens THE SYSTEM SHOULD respond": false,
		"IF the disk is full THE SYSTEM SHALL stop":      false,
		"AFTER a thing THE SYSTEM SHALL respond":         false,
		"WHEN THE SYSTEM SHALL respond":                  false,
		"WHEN a thing happens THE SYSTEM SHALL":          false,
	}
	for item, ok := range cases {
		files := goodGroup(1, 1, tgSpec{items: []string{item}})
		fs := rulesOf(lintFiles(t, files, nil, nil), RuleEARS, SeverityFail)
		if ok && len(fs) != 0 || !ok && len(fs) != 1 {
			t.Errorf("%q: findings %+v", item, fs)
		}
	}
	files := goodGroup(1, 1, tgSpec{items: []string{"WHEN a THE SYSTEM SHALL b", "WHEN c THE SYSTEM does d"}})
	fs := rulesOf(lintFiles(t, files, nil, nil), RuleEARS, SeverityFail)
	if len(fs) != 1 || fs[0].Message != "acceptance item 2 has no SHALL" || fs[0].Path != "epic-1/tg-1.1/TG.md" {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintGroupWithoutAcceptanceFailsRequired(t *testing.T) {
	files := goodGroup(1, 1, tgSpec{})
	files[0].Content = strings.Split(files[0].Content, "\n\n")[0] + "\n"
	if fs := rulesOf(lintFiles(t, files, nil, nil), RuleRequired, SeverityFail); len(fs) != 1 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintSizing(t *testing.T) {
	big := goodGroup(1, 1, tgSpec{files: []string{"a", "b", "c", "d", "e", "f"}})
	big[1].Content = taskText(100, "x", "a", "b", "c", "d", "e", "f")
	if fs := rulesOf(lintFiles(t, big, nil, nil), RuleSizing, SeverityFail); len(fs) != 1 ||
		fs[0].Path != "epic-1/tg-1.1/tsk-one.md" {
		t.Fatalf("6 files: %+v", fs)
	}
	for _, est := range []int{300, 301} {
		g := goodGroup(1, 1, tgSpec{})
		g[1].Content = taskText(est, "x", "pkg1_1/a.go")
		n := len(rulesOf(lintFiles(t, g, nil, nil), RuleSizing, SeverityFail))
		if (est == 300) != (n == 0) {
			t.Errorf("estimate %d: %d findings", est, n)
		}
	}
	nine := goodGroup(1, 1, tgSpec{})
	for i := 2; i <= 9; i++ {
		nine = append(nine, DraftFile{
			Path:    fmt.Sprintf("epic-1/tg-1.1/tsk-t%d.md", i),
			Content: taskText(10, "x", "pkg1_1/a.go"),
		})
	}
	if fs := rulesOf(lintFiles(t, nine, nil, nil), RuleSizing, SeverityFail); len(fs) != 1 ||
		fs[0].Path != "epic-1/tg-1.1/TG.md" {
		t.Fatalf("9 tasks: %+v", fs)
	}
	if fs := rulesOf(lintFiles(t, nine[:9], nil, nil), RuleSizing, SeverityFail); len(fs) != 0 {
		t.Fatalf("8 tasks: %+v", fs)
	}
	for _, p := range []string{"1", "2", "3", "5", "8", "4", "13"} {
		ok := strings.Contains("1 2 3 5 8", p) && len(p) == 1
		n := len(rulesOf(lintFiles(t, goodGroup(1, 1, tgSpec{points: p}), nil, nil), RuleSizing, SeverityFail))
		if ok != (n == 0) {
			t.Errorf("points %s: %d findings", p, n)
		}
	}
}

func TestLintRequiredFilesAndDoneWhen(t *testing.T) {
	g := goodGroup(1, 1, tgSpec{})
	g[1].Content = taskText(10, "", "outside/other.go")
	fs := rulesOf(lintFiles(t, g, nil, nil), RuleRequired, SeverityFail)
	if len(fs) != 2 {
		t.Fatalf("want done_when and outside file: %+v", fs)
	}
	g = goodGroup(1, 1, tgSpec{})
	g[0].Content = strings.Replace(g[0].Content, "files:\n- pkg1_1/a.go\n", "files:\n", 1)
	g[1].Content = "estimate_lines: 3\ndone_when: x\n\ncard"
	fs = rulesOf(lintFiles(t, g, nil, nil), RuleRequired, SeverityFail)
	if len(fs) != 2 {
		t.Fatalf("want group files and task files: %+v", fs)
	}
}

func TestLintProtected(t *testing.T) {
	cases := []struct {
		file string
		want bool
	}{
		{".github/workflows/ci.yml", true},
		{"AGENTS.md", true},
		{"docs/backlog/epic-1/tg-1.1/TG.md", true},
		{"docs/backlog/x", true},
		{"docs/hld.md", false},
		{"internal/a.go", false},
	}
	for _, c := range cases {
		files := goodGroup(1, 1, tgSpec{files: []string{c.file}})
		fs := rulesOf(lintFiles(t, files, []string{".github/**", "AGENTS.md"}, nil), RuleProtected, SeverityFail)
		if c.want != (len(fs) == 1) {
			t.Errorf("%s: %+v", c.file, fs)
		}
	}
}

func TestLintCaseCollision(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{files: []string{"Readme.md"}})
	fs := rulesOf(lintFiles(t, a, nil, []string{"README.md"}), RuleCaseCollision, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "Readme.md") || !strings.Contains(fs[0].Message, "README.md") {
		t.Fatalf("tracked: %+v", fs)
	}
	b := goodGroup(1, 2, tgSpec{files: []string{"README.md"}})
	if fs := rulesOf(lintFiles(t, cat(a, b), nil, nil), RuleCaseCollision, SeverityFail); len(fs) != 1 {
		t.Fatalf("two groups: %+v", fs)
	}
	// A collision between two tracked files alone is not the plan's doing.
	if fs := rulesOf(
		lintFiles(t, goodGroup(1, 1, tgSpec{}), nil, []string{"A.md", "a.md"}),
		RuleCaseCollision,
		SeverityFail,
	); len(fs) != 0 {
		t.Fatalf("tracked pair: %+v", fs)
	}
}

func TestLintDuplicateGroupIDFails(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{id: uid(7)})
	b := goodGroup(1, 2, tgSpec{id: uid(7)})
	if fs := rulesOf(lintFiles(t, cat(a, b), nil, nil), RuleGrammar, SeverityFail); len(fs) != 1 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestLintDraftReportsOnlyDraftGroups(t *testing.T) {
	existing := goodGroup(1, 1, tgSpec{points: "4"})
	root := t.TempDir()
	writeBacklog(t, root, existing)
	epics, _ := mustLoad(t, root)
	draft := draftOf(goodGroup(2, 1, tgSpec{deps: []string{"tg-1.1"}}))
	if fs := fails(LintDraft(draft, epics, nil, nil)); len(fs) != 0 {
		t.Fatalf("a flaw in a stored group must not fail the draft: %+v", fs)
	}
}

func TestLintDraftReplacesAmendedGroup(t *testing.T) {
	root := t.TempDir()
	writeBacklog(t, root, goodGroup(1, 1, tgSpec{files: []string{"shared/x.go"}}))
	epics, _ := mustLoad(t, root)
	// The amended tg-1.1 no longer shares the file with the new tg-1.2.
	draft := draftOf(goodGroup(1, 1, tgSpec{id: uid(101)}), goodGroup(1, 2, tgSpec{files: []string{"shared/x.go"}}))
	if fs := fails(LintDraft(draft, epics, nil, nil)); len(fs) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestOverEstimate(t *testing.T) {
	tk := Task{Name: "tsk-a", EstimateLines: 100}
	if f, ok := OverEstimate(tk, 210); !ok || f.Rule != RuleSizing || f.Severity != SeverityWarn {
		t.Fatalf("2.1 times: %+v %v", f, ok)
	}
	if _, ok := OverEstimate(tk, 200); ok {
		t.Fatal("exactly 2 times must not warn")
	}
}

// lintFiles loads files as stored task lists and lints them with the load findings.
func lintFiles(t *testing.T, files []DraftFile, protected, tracked []string) []Finding {
	t.Helper()
	root := t.TempDir()
	writeBacklog(t, root, files)
	epics, load := mustLoad(t, root)
	return sortFindings(append(load, Lint(epics, protected, tracked)...))
}
