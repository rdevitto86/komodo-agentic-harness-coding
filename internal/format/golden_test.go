package format

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"text/template/parse"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

// update rewrites the golden files from the rendered bytes instead of comparing against them.
var update = flag.Bool("update", false, "rewrite embed/output golden files from the rendered bytes")

// outputDir is the source folder of the embedded output files.
const outputDir = "../../embed/output"

// outputFS returns the embedded output files.
func outputFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(komodo.Embedded, "embed/output")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// reflectType returns the struct type a pointer to a fact struct points at.
func reflectType(v any) reflect.Type { return reflect.TypeOf(v).Elem() }

// jsonFields returns the exported field names of a struct type.
func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).IsExported() {
			out[t.Field(i).Name] = true
		}
	}
	return out
}

// parse0 parses template text with the real function map and returns every tree, defines included.
func parse0(name, text string) ([]*parse.Tree, error) {
	tm, err := parse0Template(name, text)
	if err != nil {
		return nil, err
	}
	var trees []*parse.Tree
	for _, x := range tm.Templates() {
		trees = append(trees, x.Tree)
	}
	return trees, nil
}

// parse0Template parses template text the way New does.
func parse0Template(name, text string) (*template.Template, error) { return parseTemplate(name, text) }

// renderGolden renders one shape's fixture under one profile with colour off and the closing headings on.
func renderGolden(fsys fs.FS, p Profile, shape string) ([]byte, error) {
	f, err := newFromFS(fsys, Options{Profile: p, Color: ColorOff, TurnEnd: true})
	if err != nil {
		return nil, err
	}
	facts, err := fs.ReadFile(fsys, "fixtures/"+shape+".json")
	if err != nil {
		return nil, err
	}
	code := ""
	if i := strings.Index(shape, "."); i > 0 && (strings.HasPrefix(shape, "error.") || strings.HasPrefix(shape, "needs.")) {
		code = shape[i+1:]
	}
	r, err := f.FormatJSON(msg(shape, code, string(facts)))
	return []byte(r.Report), err
}

// goldenDiffs returns one line per shape and profile whose rendered bytes differ from the golden file.
func goldenDiffs(fsys fs.FS) []string {
	var out []string
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		for _, shape := range shapeNames() {
			got, err := renderGolden(fsys, p, shape)
			if err != nil {
				out = append(out, fmt.Sprintf("%s %s: %v", p, shape, err))
				continue
			}
			want, err := fs.ReadFile(fsys, string(p)+"/"+shape+".golden")
			if err != nil || !bytes.Equal(got, want) {
				out = append(out, fmt.Sprintf("%s %s: rendered bytes differ from the golden file (%v)", p, shape, err))
			}
		}
	}
	return out
}

func TestGolden(t *testing.T) {
	if *update {
		for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
			for _, shape := range shapeNames() {
				got, err := renderGolden(outputFS(t), p, shape)
				if err != nil {
					t.Fatalf("%s %s: %v", p, shape, err)
				}
				path := filepath.Join(outputDir, string(p), shape+".golden")
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Skip("golden files rewritten")
	}
	for _, d := range goldenDiffs(outputFS(t)) {
		t.Error(d)
	}
}

func TestGoldenOneByte(t *testing.T) {
	golden, _ := fs.ReadFile(outputFS(t), "adhd/reply.run.golden")
	changed := withTemplate(t, "adhd/reply.run.golden", string(golden[:len(golden)-2])+"X\n")
	diffs := goldenDiffs(changed)
	if len(diffs) != 1 || !strings.Contains(diffs[0], "adhd reply.run") {
		t.Fatalf("diffs = %v, want exactly adhd reply.run", diffs)
	}
}

func TestEveryTemplateHasShape(t *testing.T) {
	for _, p := range []Profile{ProfileAdhd, ProfileStandard} {
		entries, err := fs.ReadDir(outputFS(t), string(p))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			shape := strings.TrimSuffix(strings.TrimSuffix(name, ".tmpl"), ".golden")
			if _, ok := shapes[shape]; !ok {
				t.Errorf("%s/%s names no registered shape", p, name)
			}
		}
		if len(entries) != 2*len(shapeNames()) {
			t.Errorf("%s holds %d files, want %d", p, len(entries), 2*len(shapeNames()))
		}
	}
}
