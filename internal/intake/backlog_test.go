package intake

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadReadsGroupsInFolderOrder(t *testing.T) {
	root := t.TempDir()
	spec := tgSpec{touches: "internal/engine/**", items: []string{
		"WHEN a write carries an older fence THE SYSTEM SHALL refuse it",
		"IF the lease is gone THEN THE SYSTEM SHALL stop",
	}}
	g := goodGroup(1, 2, spec)
	g[0].Content = strings.Replace(
		g[0].Content,
		"  cmd: go test ./...\n",
		"  cmd: go test ./...\n  needs: network, docker\n",
		1,
	)
	writeBacklog(t, root, cat(g, goodGroup(1, 10, tgSpec{}), goodGroup(1, 9, tgSpec{deps: []string{"tg-1.2"}})))
	epics, fs := mustLoad(t, root)
	if len(fs) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
	if len(epics) != 1 || epics[0].Number != 1 {
		t.Fatalf("epics: %+v", epics)
	}
	var names []string
	for _, gr := range epics[0].Groups {
		names = append(names, gr.Name)
	}
	if !reflect.DeepEqual(names, []string{"tg-1.2", "tg-1.9", "tg-1.10"}) {
		t.Fatalf("order: %v", names)
	}
	got := epics[0].Groups[0]
	if got.ID != uid(102) || got.Goal != "Group goal" || got.Points != 3 || got.Status != "todo" ||
		got.Priority != PriorityNormal {
		t.Fatalf("fields: %+v", got)
	}
	if !reflect.DeepEqual(got.Touches, []string{"internal/engine/**"}) || len(got.Files) != 1 || len(got.Dependencies) != 0 {
		t.Fatalf("lists: %+v", got)
	}
	if len(got.Acceptance) != 2 || got.Acceptance[0].Keyword != "WHEN" || got.Acceptance[1].Keyword != "IF" ||
		got.Acceptance[0].Text != "a write carries an older fence THE SYSTEM SHALL refuse it" ||
		got.Acceptance[0].Cmd != "go test ./..." {
		t.Fatalf("acceptance: %+v", got.Acceptance)
	}
	if !reflect.DeepEqual(got.Acceptance[0].Needs, []string{"network", "docker"}) {
		t.Fatalf("needs: %+v", got.Acceptance[0].Needs)
	}
	if tk := got.Tasks[0]; tk.Name != "tsk-one" || tk.DoneWhen != "go test ./..." || tk.EstimateLines != 100 ||
		tk.Card != "Write the thing." {
		t.Fatalf("task: %+v", tk)
	}
	if !reflect.DeepEqual(epics[0].Groups[1].Dependencies, []string{"tg-1.2"}) {
		t.Fatalf("deps: %+v", epics[0].Groups[1].Dependencies)
	}
}

func TestLoadMissingBacklogIsEmpty(t *testing.T) {
	epics, fs := mustLoad(t, t.TempDir())
	if len(epics) != 0 || len(fs) != 0 {
		t.Fatalf("got %+v %+v", epics, fs)
	}
}

func TestLoadIgnoresPlanFoldersAndTopLevelFiles(t *testing.T) {
	root := t.TempDir()
	writeBacklog(t, root, goodGroup(1, 1, tgSpec{}))
	junk := []DraftFile{
		{Path: ".plan-abc/epic-2/tg-2.1/TG.md", Content: "garbage"},
		{Path: ".plan-old-abc/epic-1/tg-1.1/TG.md", Content: "garbage"},
		{Path: ".plan-failed-abc/epic-3/tg-3.1/TG.md", Content: "garbage"},
		{Path: "README.md", Content: "notes"},
	}
	writeBacklog(t, root, junk)
	epics, fs := mustLoad(t, root)
	if len(epics) != 1 || len(fs) != 0 {
		t.Fatalf("got %+v %+v", epics, fs)
	}
}

func TestLoadUnreadableFileIsError(t *testing.T) {
	root := t.TempDir()
	writeBacklog(t, root, goodGroup(1, 1, tgSpec{}))
	p := filepath.Join(root, "docs", "backlog", "epic-1", "tg-1.1", "TG.md")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere"), p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Fatal("want an error for a file that cannot be read")
	}
}

func TestGrammarFindingsNameTheFile(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{
			"unknown key",
			func(s string) string { return strings.Replace(s, "subsystem: none", "subsystem: none\nowner: me", 1) },
			"owner",
		},
		{
			"duplicate key",
			func(s string) string { return strings.Replace(s, "goal: Group goal", "goal: a\ngoal: b", 1) },
			"goal",
		},
		{"upper case key", func(s string) string { return strings.Replace(s, "goal:", "Goal:", 1) }, "Goal"},
		{"bad line", func(s string) string { return strings.Replace(s, "subsystem: none", "not a field", 1) }, "line 3"},
		{"missing goal", func(s string) string { return strings.Replace(s, "goal: Group goal\n", "", 1) }, "goal"},
		{"bad id", func(s string) string { return strings.Replace(s, uid(101), "0192e4c5-...", 1) }, "id"},
		{"missing points", func(s string) string { return strings.Replace(s, "points: 3\n", "", 1) }, "points"},
		{
			"points not a number",
			func(s string) string { return strings.Replace(s, "points: 3", "points: three", 1) },
			"points",
		},
		{
			"bad priority",
			func(s string) string { return strings.Replace(s, "priority: normal", "priority: urgent", 1) },
			"priority",
		},
		{"bad status", func(s string) string { return strings.Replace(s, "status: todo", "status: nope", 1) }, "status"},
		{"bad needs", func(s string) string { return strings.Replace(s, "  cmd:", "  needs: wifi\n  cmd:", 1) }, "wifi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := goodGroup(1, 1, tgSpec{})
			files[0].Content = c.mutate(files[0].Content)
			fs := rulesOf(LintDraft(draftOf(files), nil, nil, nil), RuleGrammar, SeverityFail)
			if len(fs) == 0 {
				t.Fatal("no grammar finding")
			}
			if fs[0].Path != "epic-1/tg-1.1/TG.md" || !strings.Contains(fs[0].Message, c.want) {
				t.Fatalf("finding: %+v", fs[0])
			}
		})
	}
}

func TestMissingDependenciesKeyIsRequired(t *testing.T) {
	files := goodGroup(1, 1, tgSpec{noDeps: true})
	fs := LintDraft(draftOf(files), nil, nil, nil)
	if got := rulesOf(fs, RuleRequired, SeverityFail); len(got) != 1 || !strings.Contains(got[0].Message, "dependencies") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestTaskGrammar(t *testing.T) {
	cases := map[string]string{
		"unknown key":      "files: pkg1_1/a.go\ndone_when: x\nestimate_lines: 5\nowner: me\n\ncard",
		"missing estimate": "files: pkg1_1/a.go\ndone_when: x\n\ncard",
		"bad estimate":     "files: pkg1_1/a.go\ndone_when: x\nestimate_lines: lots\n\ncard",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			files := goodGroup(1, 1, tgSpec{})
			files[1].Content = content
			fs := rulesOf(LintDraft(draftOf(files), nil, nil, nil), RuleGrammar, SeverityFail)
			if len(fs) == 0 || fs[0].Path != "epic-1/tg-1.1/tsk-one.md" {
				t.Fatalf("findings: %+v", fs)
			}
		})
	}
}

func TestDraftPathsAreChecked(t *testing.T) {
	for _, p := range []string{
		"epic-1/../../etc/x.md",
		"/abs/epic-1/tg-1.1/TG.md",
		"epic-1/tg-1.1/notes.md",
		"epic-1/tg-2.1/TG.md",
		"epic-x/tg-1.1/TG.md",
		"TG.md",
	} {
		files := goodGroup(1, 1, tgSpec{})
		files = append(files, DraftFile{Path: p, Content: "files: a\n"})
		fs := rulesOf(LintDraft(draftOf(files), nil, nil, nil), RuleGrammar, SeverityFail)
		found := false
		for _, f := range fs {
			if f.Path == p {
				found = true
			}
		}
		if !found {
			t.Errorf("path %q: no grammar finding in %+v", p, fs)
		}
	}
}

func TestDraftWithoutGroupFileOrGroupFails(t *testing.T) {
	d := PlanDraft{Files: []DraftFile{{Path: "epic-1/tg-1.1/tsk-one.md", Content: taskText(1, "x", "a")}}}
	if len(rulesOf(LintDraft(d, nil, nil, nil), RuleGrammar, SeverityFail)) == 0 {
		t.Fatal("a task file with no TG.md must fail")
	}
	if len(fails(LintDraft(PlanDraft{}, nil, nil, nil))) == 0 {
		t.Fatal("an empty draft must fail")
	}
	files := goodGroup(1, 1, tgSpec{})
	if len(rulesOf(LintDraft(draftOf(files[:1]), nil, nil, nil), RuleGrammar, SeverityFail)) == 0 {
		t.Fatal("a group with no task file must fail")
	}
}

func TestDuplicateDraftPathFails(t *testing.T) {
	files := goodGroup(1, 1, tgSpec{})
	files = append(files, files[0])
	if len(rulesOf(LintDraft(draftOf(files), nil, nil, nil), RuleGrammar, SeverityFail)) == 0 {
		t.Fatal("a repeated path must fail")
	}
}
