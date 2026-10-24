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

// withGolangci stubs the golangci-lint lookup for one test: found true puts the tool on PATH.
func withGolangci(t *testing.T, found bool) {
	t.Helper()

	old := lookGolangci
	lookGolangci = func() (string, error) {
		if found {
			return "/usr/local/bin/golangci-lint", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookGolangci = old })
}

func TestDetectGoLintGolangci(t *testing.T) {
	vet := []string{"go", "vet", "./..."}
	run := []string{"golangci-lint", "run"}

	for _, tc := range []struct {
		name   string
		config string
		found  bool
		want   []string
	}{
		{"yml and tool", ".golangci.yml", true, run},
		{"yaml and tool", ".golangci.yaml", true, run},
		{"toml and tool", ".golangci.toml", true, run},
		{"json and tool", ".golangci.json", true, run},
		{"config without tool", ".golangci.yml", false, vet},
		{"tool without config", "", true, vet},
		{"neither", "", false, vet},
		{"config in a folder", "sub/.golangci.yml", true, vet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withGolangci(t, tc.found)

			files := map[string]string{"go.mod": "module x\n"}
			if tc.config != "" {
				files[tc.config] = "version: \"2\"\n"
			}

			cs := Detect(newTree(files))
			if got := argvOf(cs, ClassLint); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("lint: %v, want %v", got, tc.want)
			}
			for _, c := range cs.Commands {
				if c.Class == ClassLint && (c.Marker != "go.mod" || c.PerFile) {
					t.Fatalf("lint command: %+v", c)
				}
			}
		})
	}
}

func TestDetectGoLintMakefileWins(t *testing.T) {
	withGolangci(t, true)

	cs := Detect(newTree(map[string]string{
		"go.mod": "module x\n", ".golangci.yml": "version: \"2\"\n", "Makefile": "lint:\n\techo\n",
	}))
	if got := argvOf(cs, ClassLint); !reflect.DeepEqual(got, []string{"make", "lint"}) {
		t.Fatalf("lint: %v", got)
	}
}

func TestDetectGoLintOnlyForGo(t *testing.T) {
	withGolangci(t, true)

	cs := Detect(newTree(map[string]string{"pyproject.toml": "[project]\n", ".golangci.yml": "version: \"2\"\n"}))
	if got := argvOf(cs, ClassLint); !reflect.DeepEqual(got, []string{"ruff", "check"}) {
		t.Fatalf("lint: %v", got)
	}
}

func TestDetectGoLintHashFollowsConfigAndTool(t *testing.T) {
	files := func(config string) *fakeTree {
		f := map[string]string{"go.mod": "module x\n"}
		if config != "" {
			f[".golangci.yml"] = config
		}
		return newTree(f)
	}

	withGolangci(t, false)
	without := Detect(files("a")).MarkerHash
	other := Detect(files("b")).MarkerHash

	withGolangci(t, true)
	with := Detect(files("a")).MarkerHash

	if without == other || without == with {
		t.Fatal("the hash must follow the config file's content and the tool's presence")
	}
	if Detect(files("")).MarkerHash != Detect(newTree(map[string]string{"go.mod": "module x\n"})).MarkerHash {
		t.Fatal("a repository with no config must hash as before")
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
