package contextmgr

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var errNoRead = errors.New("read failed")

// argvOf returns the argv of the command of a class, or nil.
func argvOf(cs CommandSet, c Class) []string {
	for _, cmd := range cs.Commands {
		if cmd.Class == c {
			return cmd.Argv
		}
	}
	return nil
}

func TestDetectGo(t *testing.T) {
	cs := Detect(goTree())
	want := map[Class][]string{
		ClassSetup: {"go", "mod", "download"}, ClassBuild: {"go", "build", "./..."}, ClassTest: {"go", "test", "./..."},
		ClassLint: {"go", "vet", "./..."}, ClassFormat: {"gofmt", "-w"},
	}
	for c, argv := range want {
		if got := argvOf(cs, c); !reflect.DeepEqual(got, argv) {
			t.Errorf("%s: %v", c, got)
		}
	}
	if len(cs.MarkerHash) != 64 {
		t.Fatalf("marker hash %q", cs.MarkerHash)
	}
	var git int
	for _, c := range cs.Commands {
		if c.Class == ClassGitRead {
			git++
		}
	}
	if git != 4 {
		t.Fatalf("git-read commands: %d", git)
	}
}

func TestDetectMakefileTargetWins(t *testing.T) {
	tree := newTree(map[string]string{"go.mod": "module x\n", "Makefile": "test:\n\tgo test ./...\n\nlint:\n\techo\n"})
	cs := Detect(tree)
	for _, c := range cs.Commands {
		switch c.Class {
		case ClassTest, ClassLint:
			if c.Marker != "Makefile" || c.Argv[0] != "make" {
				t.Errorf("%s: %+v", c.Class, c)
			}
		case ClassBuild:
			if c.Marker != "go.mod" {
				t.Errorf("build: %+v", c)
			}
		}
	}
	if got := argvOf(cs, ClassTest); !reflect.DeepEqual(got, []string{"make", "test"}) {
		t.Fatalf("test: %v", got)
	}
	if got := argvOf(
		Detect(newTree(map[string]string{"Makefile": "fmt:\n"})),
		ClassFormat,
	); !reflect.DeepEqual(got, []string{"make", "fmt"}) {
		t.Fatalf("format target is fmt: %v", got)
	}
}

func TestDetectEmptyClass(t *testing.T) {
	cs := Detect(newTree(map[string]string{"pyproject.toml": "[project]\n"}))
	if argvOf(cs, ClassSetup) != nil || argvOf(cs, ClassBuild) != nil {
		t.Fatalf("pyproject has no setup or build: %+v", cs.Commands)
	}
	if got := argvOf(cs, ClassTest); !reflect.DeepEqual(got, []string{"pytest"}) {
		t.Fatalf("test: %v", got)
	}
}

func TestDetectMarkerHash(t *testing.T) {
	a := Detect(newTree(map[string]string{"go.mod": "module a\n"}))
	b := Detect(newTree(map[string]string{"go.mod": "module b\n"}))
	c := Detect(newTree(map[string]string{"go.mod": "module a\n", "x.go": "package x"}))
	if a.MarkerHash == b.MarkerHash || a.MarkerHash != c.MarkerHash {
		t.Fatal("the hash must follow marker files only")
	}
}

func TestRenderProfile(t *testing.T) {
	out := renderProfile([]string{"go", "python"}, Detect(goTree()))
	for _, want := range []string{
		"Languages: go, python",
		"setup: go mod download",
		"test: go test ./...",
		"git-read: git status, git diff, git log, git show",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if !strings.Contains(renderProfile(nil, CommandSet{}), "Languages: none") {
		t.Error("no languages renders none")
	}
}

func TestDetectLanguages(t *testing.T) {
	langs := shippedLangs(t)
	got := detectLanguages(mapFixture(), langs)
	if !reflect.DeepEqual(got, []string{"go", "python"}) {
		t.Fatalf("got %v", got)
	}
}
