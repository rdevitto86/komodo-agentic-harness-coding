package contextmgr

import (
	"reflect"
	"strings"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

func mapFixture() *fakeTree {
	return newTree(map[string]string{
		"a.go":      "package a\n\nfunc Alpha() {}\n\ntype Thing struct{}\n\nfunc (t Thing) Run() {}\n",
		"b.go":      "package b\n\nfunc Beta() { Alpha() }\n",
		"c.go":      "package c\n\nfunc Gamma() { Alpha(); Beta() }\n",
		"d.py":      "def delta():\n    pass\n\nclass Echo:\n    pass\n",
		"e.py":      "from d import delta\n\ndef zed():\n    delta()\n",
		"README.md": "Alpha is documented here.\n",
	})
}

func shippedLangs(t *testing.T) []langPattern {
	t.Helper()
	langs, err := loadLangs(komodo.Embedded)
	if err != nil {
		t.Fatalf("loadLangs: %v", err)
	}
	return langs
}

func TestRepoMapRankAndRender(t *testing.T) {
	rm := buildRepoMap(mapFixture(), "", shippedLangs(t))
	if rm.TreeHash == "" || len(rm.Files) != 6 {
		t.Fatalf("map %+v", rm)
	}
	var order []string
	ranks := map[string]int{}
	for _, f := range rm.Files {
		order = append(order, f.Path)
		ranks[f.Path] = f.Rank
	}
	// Alpha is named by b.go and c.go; Beta by c.go; delta by e.py.
	want := map[string]int{"a.go": 2, "b.go": 1, "c.go": 0, "d.py": 1, "e.py": 0, "README.md": 0}
	if !reflect.DeepEqual(ranks, want) {
		t.Fatalf("ranks %v", ranks)
	}
	if !reflect.DeepEqual(order, []string{"a.go", "b.go", "d.py", "README.md", "c.go", "e.py"}) {
		t.Fatalf("order %v", order)
	}
	out := rm.Render()
	if !strings.HasPrefix(out, "a.go (2)\n  function Alpha\n  type Thing\n  function Run\n") || !strings.Contains(out, "d.py (1)\n  function delta\n  class Echo\n") || !strings.HasSuffix(out, "e.py (0)\n  function zed") {
		t.Fatalf("render:\n%s", out)
	}
	if again := buildRepoMap(mapFixture(), "", shippedLangs(t)).Render(); again != out {
		t.Fatal("two runs must be byte-identical")
	}
}

func TestRepoMapGoalBoost(t *testing.T) {
	rm := buildRepoMap(mapFixture(), "please change b.go and Zed", shippedLangs(t))
	ranks := map[string]int{}
	for _, f := range rm.Files {
		ranks[f.Path] = f.Rank
	}
	if ranks["b.go"] != 3 || ranks["a.go"] != 2 || ranks["e.py"] != 0 {
		t.Fatalf("ranks %v", ranks)
	}
	if rm.Files[0].Path != "b.go" {
		t.Fatalf("goal file should lead: %v", rm.Files[0])
	}
}

func TestRepoMapFit(t *testing.T) {
	rm := buildRepoMap(mapFixture(), "", shippedLangs(t))
	full := CountTokens(rm.Render())
	fit := rm.Fit(full - 1)
	if len(fit.Files) >= len(rm.Files) || CountTokens(fit.Render()) > full-1 {
		t.Fatalf("fit kept %d files at %d tokens", len(fit.Files), CountTokens(fit.Render()))
	}
	if fit.Files[0].Path != "a.go" {
		t.Fatalf("the lowest ranks drop first: %v", fit.Files)
	}
	if got := rm.Fit(0).Render(); got != rm.Render() {
		t.Fatal("a zero budget means no fitting")
	}
	if got := rm.Fit(1).Render(); got != noFiles {
		t.Fatalf("nothing fits: %q", got)
	}
}

func TestRepoMapUnreadableSkipped(t *testing.T) {
	tree := mapFixture()
	tree.onRead = func(p string) {}
	delete(tree.files, "README.md")
	rm := buildRepoMap(&unreadable{tree, "b.go"}, "", shippedLangs(t))
	for _, f := range rm.Files {
		if f.Path == "b.go" {
			t.Fatal("an unreadable file must be skipped")
		}
	}
}

type unreadable struct {
	*fakeTree
	bad string
}

func (u *unreadable) ReadFile(p string) ([]byte, error) {
	if p == u.bad {
		return nil, errNoRead
	}
	return u.fakeTree.ReadFile(p)
}

func TestShippedLangPatterns(t *testing.T) {
	langs := shippedLangs(t)
	byName := map[string]langPattern{}
	for _, l := range langs {
		byName[l.Name] = l
	}
	samples := map[string]struct{ file, text, sym string }{
		"python":     {"x.py", "def run_it(a):", "run_it"},
		"rust":       {"x.rs", "pub fn open(x: u8) {", "open"},
		"typescript": {"x.ts", "export function load() {", "load"},
	}
	for name, s := range samples {
		l, ok := byName[name]
		if !ok {
			t.Fatalf("no pattern file for %s", name)
		}
		tree := newTree(map[string]string{s.file: s.text + "\n"})
		rm := buildRepoMap(tree, "", []langPattern{l})
		found := false
		for _, sy := range rm.Files[0].Symbols {
			found = found || sy.Name == s.sym
		}
		if !found {
			t.Errorf("%s: symbol %q not found in %+v", name, s.sym, rm.Files[0].Symbols)
		}
	}
}
