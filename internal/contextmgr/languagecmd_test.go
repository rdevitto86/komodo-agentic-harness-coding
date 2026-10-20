package contextmgr

import (
	"reflect"
	"testing"
)

func TestLanguageCommandIgnoresMakefileTargets(t *testing.T) {
	tree := newTree(map[string]string{
		"go.mod":   "module x\n",
		"Makefile": "build:\n\tdocker build .\n\nlint:\n\tgolangci-lint run\n",
	})

	for c, want := range map[Class][]string{ClassBuild: {"go", "build", "./..."}, ClassLint: {"go", "vet", "./..."}} {
		got, ok := LanguageCommand(tree, c)
		if !ok || got.Marker != "go.mod" || !reflect.DeepEqual(got.Argv, want) {
			t.Errorf("%s: %+v, %v", c, got, ok)
		}
	}
}

func TestLanguageCommandNodeNeedsTheScript(t *testing.T) {
	tree := newTree(map[string]string{"package.json": `{"scripts":{"build":"tsc"}}`})

	if got, ok := LanguageCommand(tree, ClassBuild); !ok || !reflect.DeepEqual(got.Argv, []string{"npm", "run", "build"}) {
		t.Errorf("build: %+v, %v", got, ok)
	}
	if got, ok := LanguageCommand(tree, ClassLint); ok {
		t.Errorf("lint without a script: %+v", got)
	}
}

func TestLanguageCommandNoMarkerHasNone(t *testing.T) {
	if got, ok := LanguageCommand(newTree(map[string]string{"Makefile": "build:\n"}), ClassBuild); ok {
		t.Errorf("Makefile only: %+v", got)
	}
}
