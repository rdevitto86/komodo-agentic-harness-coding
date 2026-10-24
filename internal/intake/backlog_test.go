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
