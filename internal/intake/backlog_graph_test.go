package intake

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWavesAndLongestChain(t *testing.T) {
	files := cat(
		goodGroup(1, 1, tgSpec{}),
		goodGroup(1, 2, tgSpec{}),
		goodGroup(1, 3, tgSpec{deps: []string{"tg-1.1", "tg-1.2"}}),
		goodGroup(1, 4, tgSpec{deps: []string{"tg-1.3"}}),
	)
	_, epics := loadFiles(t, files)
	g, fs := buildGraphEpics(epics, nil)
	if len(fs) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
	want := [][]string{{"tg-1.1", "tg-1.2"}, {"tg-1.3"}, {"tg-1.4"}}
	if got := g.Waves(); !reflect.DeepEqual(got, want) {
		t.Fatalf("waves: %v", got)
	}
	if n := g.LongestChain(); n != 3 {
		t.Fatalf("chain: %d", n)
	}
}

func TestOverlappingTouchesNeedAnOrder(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{touches: "internal/lease/**", files: []string{"internal/lease/a.go"}})
	b := goodGroup(1, 2, tgSpec{touches: "internal/lease/*.go", files: []string{"internal/lease/b.go"}})
	_, epics := loadFiles(t, cat(a, b))
	_, fs := buildGraphEpics(epics, []string{"internal/lease/fence.go"})
	got := rulesOf(fs, RuleGraph, SeverityFail)
	if len(got) != 1 || !strings.Contains(got[0].Message, "internal/lease/a.go") ||
		!strings.Contains(got[0].Message, "tg-1.1") || !strings.Contains(got[0].Message, "tg-1.2") {
		t.Fatalf("findings: %+v", fs)
	}
	// An order, stated on the draft, clears the finding.
	b = goodGroup(1, 2, tgSpec{
		touches: "internal/lease/*.go",
		files:   []string{"internal/lease/b.go"},
		deps:    []string{"tg-1.1"},
	})
	_, epics = loadFiles(t, cat(a, b))
	if _, fs := buildGraphEpics(epics, nil); len(fails(fs)) != 0 {
		t.Fatalf("ordered pair: %+v", fs)
	}
}

func TestOverlapOrderedThroughAThirdGroupCounts(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{touches: "x/**", files: []string{"x/a.go"}})
	mid := goodGroup(1, 2, tgSpec{deps: []string{"tg-1.1"}})
	c := goodGroup(1, 3, tgSpec{touches: "x/**", files: []string{"x/c.go"}, deps: []string{"tg-1.2"}})
	_, epics := loadFiles(t, cat(a, mid, c))
	if _, fs := buildGraphEpics(epics, nil); len(fails(fs)) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestDraftOmittingDerivedDependencyFailsNamingTheFile(t *testing.T) {
	a := goodGroup(1, 1, tgSpec{touches: "internal/engine/**", files: []string{"internal/engine/a.go"}})
	b := goodGroup(1, 2, tgSpec{touches: "internal/engine/**", files: []string{"internal/engine/b.go"}})
	fs := rulesOf(lintDraftBacklog(draftOf(a, b), nil, nil, []string{"internal/engine/zz.go"}), RuleGraph, SeverityFail)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "internal/engine/a.go") {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestCardHashIgnoresStatusAndSeesEveryByte(t *testing.T) {
	root, epics := loadFiles(t, cat(goodGroup(1, 1, tgSpec{})))
	g := epics[0].Groups[0]
	base, err := cardHashDir(root, g)
	if err != nil || !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Fatalf("hash %q err %v", base, err)
	}
	dir := filepath.Join(root, "docs", "backlog", "epic-1", "tg-1.1")
	edit := func(name, from, to string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), from) {
			t.Fatalf("%s lacks %q", name, from)
		}
		if err := os.WriteFile(
			filepath.Join(dir, name),
			[]byte(strings.Replace(string(b), from, to, 1)),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	edit("TG.md", "status: todo", "status: in_progress")
	if got, _ := cardHashDir(root, g); got != base {
		t.Fatal("a status change moved the hash")
	}
	edit("tsk-one.md", "Write the thing.", "Write the thing!")
	changed, _ := cardHashDir(root, g)
	if changed == base {
		t.Fatal("a one-byte task edit left the hash")
	}
	edit("TG.md", "goal: Group goal", "goal: Group goaL")
	if got, _ := cardHashDir(root, g); got == changed {
		t.Fatal("a goal edit left the hash")
	}
}

func TestCardHashErrors(t *testing.T) {
	if _, err := cardHashDir(t.TempDir(), Group{Name: "tg-1.1"}); err == nil {
		t.Fatal("a missing group folder must be an error")
	}
	if _, err := cardHashDir(t.TempDir(), Group{Name: "odd"}); err == nil {
		t.Fatal("a bad group name must be an error")
	}
}

// loadFiles writes files under a temporary root and loads them.
func loadFiles(t *testing.T, files []DraftFile) (string, []Epic) {
	t.Helper()
	root := t.TempDir()
	writeBacklog(t, root, files)
	epics, fs := mustLoad(t, root)
	if len(fails(fs)) != 0 {
		t.Fatalf("load findings: %+v", fs)
	}
	return root, epics
}
