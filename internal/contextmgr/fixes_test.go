package contextmgr

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// commandOf returns the command of a class, or nil.
func commandOf(cs CommandSet, c Class) *Command {
	for i := range cs.Commands {
		if cs.Commands[i].Class == c {
			return &cs.Commands[i]
		}
	}
	return nil
}

func TestDetectFormatPerFile(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		argv    []string
		perFile bool
	}{
		{"go", map[string]string{"go.mod": "module x\n"}, []string{"gofmt", "-w"}, true},
		{"rust", map[string]string{"Cargo.toml": "[package]\n"}, []string{"cargo", "fmt", "--"}, true},
		{"python", map[string]string{"pyproject.toml": "[project]\n"}, []string{"ruff", "format"}, true},
		{
			"prettier",
			map[string]string{"package.json": `{"devDependencies":{"prettier":"3.0.0"}}`},
			[]string{"npx", "prettier", "--write"},
			true,
		},
		{
			"script",
			map[string]string{"package.json": `{"scripts":{"format":"prettier ."}}`},
			[]string{"npm", "run", "format", "--"},
			true,
		},
		{"make", map[string]string{"Makefile": "fmt:\n\tgofmt -w .\n"}, []string{"make", "fmt"}, false},
	}
	for _, c := range cases {
		cmd := commandOf(Detect(newTree(c.files)), ClassFormat)
		if cmd == nil || !reflect.DeepEqual(cmd.Argv, c.argv) || cmd.PerFile != c.perFile {
			t.Errorf("%s: got %+v", c.name, cmd)
		}
	}
}

func TestDetectPackageScripts(t *testing.T) {
	only := Detect(newTree(map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}))
	if commandOf(only, ClassTest) == nil || commandOf(only, ClassBuild) != nil || commandOf(only, ClassLint) != nil ||
		commandOf(only, ClassFormat) != nil {
		t.Fatalf("only the test script is defined: %+v", only.Commands)
	}
	if commandOf(only, ClassSetup) == nil {
		t.Fatal("setup needs no script")
	}
	all := Detect(newTree(map[string]string{"package.json": `{"scripts":{"build":"a","lint":"b","test":"c"}}`}))
	if got := commandOf(all, ClassBuild); got == nil || !reflect.DeepEqual(got.Argv, []string{"npm", "run", "build"}) {
		t.Fatalf("build: %+v", got)
	}
	if got := commandOf(all, ClassLint); got == nil || !reflect.DeepEqual(got.Argv, []string{"npm", "run", "lint"}) {
		t.Fatalf("lint: %+v", got)
	}
	broken := Detect(newTree(map[string]string{"package.json": `{`}))
	if commandOf(broken, ClassTest) != nil || commandOf(broken, ClassBuild) != nil {
		t.Fatalf("an unreadable package.json defines no script: %+v", broken.Commands)
	}
}

func TestRenderProfileShowsFile(t *testing.T) {
	out := renderProfile([]string{"go"}, Detect(goTree()))
	if !strings.Contains(out, "format: gofmt -w <file>") {
		t.Fatalf("profile:\n%s", out)
	}
}

func TestReloadKeepsTableOrder(t *testing.T) {
	home := t.TempDir()
	writeLocalSkill(t, home, "standards-mine", localSkillText("standards-mine", ""))
	items := []ItemState{
		{Name: "standards-mine", Origin: OriginLocal, Enabled: true, Allow: workers, Always: []string{"builder"}},
		{Name: "standards-comments", Origin: OriginEmbedded, Enabled: true, Allow: workers, Always: []string{"builder"}},
		{Name: "standards-go", Origin: OriginEmbedded, Enabled: true, Allow: workers},
	}
	m := newManager(t, func(d *Deps) { d.Home, d.Items = home, items })
	want := []string{"standards-mine", "standards-comments", "standards-go"}
	before, _ := m.Select("builder", []string{"a.go"}, goTree())
	if !reflect.DeepEqual(names(before), want) {
		t.Fatalf("before reload: %v", names(before))
	}
	m.apply(reloadSkills())
	after, _ := m.Select("builder", []string{"a.go"}, goTree())
	if !reflect.DeepEqual(names(after), want) {
		t.Fatalf("after reload: %v", names(after))
	}
}

func TestEventDoesNotWaitForSlowReads(t *testing.T) {
	m := newManager(t)
	tree := goTree()
	entered, release := make(chan struct{}), make(chan struct{})
	tree.onRead = func(p string) {
		if p == "AGENTS.md" {
			close(entered)
			<-release
		}
	}
	type res struct {
		c   Context
		err error
	}
	out := make(chan res, 1)
	go func() {
		c, err := m.GetContext(context.Background(), Request{
			Agent: "builder",
			Tree:  tree,
			Files: []string{"a.go"},
			Slots: slotsFor("builder"),
		})
		out <- res{c, err}
	}()
	<-entered
	applied := make(chan struct{})
	go func() {
		m.apply(setEvent(SkillSet{Name: "standards-go", Enabled: false, Allow: workers}))
		close(applied)
	}()
	select {
	case <-applied:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("an event waited for the AGENTS.md read of a brief")
	}
	close(release)
	r := <-out
	if r.err != nil || !reflect.DeepEqual(names(r.c.Skills), []string{"standards-comments", "standards-go"}) {
		t.Fatalf("the brief must keep the skills it selected: %v %v", names(r.c.Skills), r.err)
	}
	if _, err := m.Get("builder", "standards-go"); err == nil {
		t.Fatal("event must be applied")
	}
}

// wholePathTree matches markers against the whole path only, as a git-backed tree may.
type wholePathTree struct{ *fakeTree }

// Exists matches the pattern against whole paths.
func (w wholePathTree) Exists(pattern string) bool {
	for p := range w.files {
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}

func TestMarkersUseSharedGlob(t *testing.T) {
	m := newManager(t)
	tree := wholePathTree{newTree(map[string]string{"app/AndroidManifest.xml": "", "src/Svc.kt": "class Svc"})}
	sel, err := m.Select("builder", []string{"src/Svc.kt"}, tree)
	if err != nil ||
		!reflect.DeepEqual(names(sel), []string{"standards-comments", "standards-kotlin", "standards-ui-mobile"}) {
		t.Fatalf("got %v %v", names(sel), err)
	}
}

func TestTemplateRefusesPartSlots(t *testing.T) {
	for _, s := range []string{"standards", "repo_context"} {
		text := strings.Replace(builderTpl, "{{diff}}", "{{"+s+"}}", 1)
		if _, err := ParseTemplate("builder", text); err == nil {
			t.Errorf("a hole for %s duplicates part 3 or 4", s)
		}
	}
}

// bigTree is a monorepo with n Go files under 10 services and 7 packages each.
func bigTree(n int) *fakeTree {
	files := map[string]string{"go.mod": "module x\n"}
	for i := 0; i < n; i++ {
		files[fmt.Sprintf("svc%02d/pkg%02d/f%04d.go", i%10, i%7, i)] = "package p\n"
	}
	return newTree(files)
}

var filesRe = regexp.MustCompile(`\((\d+) files?\)$`)

// layoutTotal counts the files a layout accounts for: plain lines plus the collapsed counts.
func layoutTotal(t *testing.T, out string) int {
	t.Helper()
	total := 0
	for _, line := range strings.Split(out, "\n") {
		if m := filesRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			total += n
		} else {
			total++
		}
	}
	return total
}

func TestLayoutBudget(t *testing.T) {
	tree := bigTree(2000)
	if full := renderLayout(tree, 0); layoutTotal(t, full) != 2001 || strings.Contains(full, " files)") {
		t.Fatal("a zero budget lists every path")
	}
	small := renderLayout(tree, 200)
	if CountTokens(small) > 200 || !strings.Contains(small, "svc00/ (") || layoutTotal(t, small) != 2001 {
		t.Fatalf("budget 200:\n%s", small)
	}
	mid := renderLayout(tree, 1000)
	if CountTokens(mid) > 1000 || !strings.Contains(mid, "svc00/pkg00/ (") || layoutTotal(t, mid) != 2001 {
		t.Fatalf("budget 1000 should keep the deepest depth that fits:\n%s", mid)
	}
	if renderLayout(newTree(map[string]string{}), 100) != noFiles {
		t.Fatal("an empty tree renders no source files")
	}
}

func TestPlannerLayoutFitsChain(t *testing.T) {
	m := newManager(t)
	req := Request{Agent: "planner", Tree: bigTree(2000), Slots: slotsFor("planner")}
	free, err := m.GetContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Chain = []Candidate{{"claude-cli", 20000}}
	fit, err := m.GetContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(free.Brief.Parts[4], " files)") || !strings.Contains(fit.Brief.Parts[4], " files)") {
		t.Fatal("the layout must collapse only when a chain gives a budget")
	}
	if _, err := fit.Brief.Place(req.Chain, nil); err != nil {
		t.Fatalf("a budgeted planner brief should fit its link: %v", err)
	}
}

func indexOf(rm RepoMap, p string) int {
	for i, f := range rm.Files {
		if f.Path == p {
			return i
		}
	}
	return -1
}

func TestRepoMapNonSourceLast(t *testing.T) {
	tree := mapFixture()
	tree.files["AAA.txt"] = "plain"
	rm := buildRepoMap(tree, "", shippedLangs(t))
	for _, src := range []string{"c.go", "e.py"} {
		for _, doc := range []string{"AAA.txt", "README.md"} {
			if indexOf(rm, src) > indexOf(rm, doc) {
				t.Errorf("%s (source, rank 0) must sort before %s", src, doc)
			}
		}
	}
}

func TestRepoMapGoalBoostAtZero(t *testing.T) {
	rm := buildRepoMap(mapFixture(), "touch e.py and see README.md", shippedLangs(t))
	if f := rm.Files[indexOf(rm, "e.py")]; f.Rank < 1 || indexOf(rm, "e.py") > indexOf(rm, "c.go") {
		t.Errorf("e.py names a rank 0 file: %+v at %d", f, indexOf(rm, "e.py"))
	}
	if indexOf(rm, "README.md") > indexOf(rm, "c.go") {
		t.Errorf("a document the goal names should not sort last: %d", indexOf(rm, "README.md"))
	}
}

func TestFindPhraseLinear(t *testing.T) {
	text := strings.Repeat("ignore previous instructionsx ", 15000)
	start := time.Now()
	if _, hit := findPhrase(text, steerList); hit {
		t.Fatal("no boundary match expected")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("scan took %v", d)
	}
}

func TestNoRedeclaredHelpers(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && (fd.Name.Name == "containsStr" || fd.Name.Name == "hashHex") {
				t.Errorf("%s declares %s, which repeats slices.Contains or fingerprint", f, fd.Name.Name)
			}
		}
	}
}

// reloadSkills returns a skill / reload event.
func reloadSkills() bus.Event {
	return bus.Event{Type: bus.TypeSkill, Subtype: "reload", Data: &SkillReload{}}
}
