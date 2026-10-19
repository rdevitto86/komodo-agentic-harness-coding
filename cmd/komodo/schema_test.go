package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// update rewrites embed/event.schema.json from the registry instead of comparing against it.
var update = flag.Bool("update", false, "rewrite embed/event.schema.json from the registry")

// schemaPath is the checked-in schema, relative to this package.
const schemaPath = "../../embed/event.schema.json"

// The checked-in schema equals the one built from every registered event; run with -update to rewrite it.
func TestSchemaMatchesEveryRegisteredEvent(t *testing.T) {
	registerAllEvents.Do(registerEvents)

	want, err := bus.Schema()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(schemaPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("embed/event.schema.json disagrees with the registered events; run: go test ./cmd/komodo -run " +
			"TestSchemaMatchesEveryRegisteredEvent -update")
	}
}

// Every registered type and subtype pair has a variant in the checked-in schema.
func TestSchemaFileNamesEveryRegisteredEvent(t *testing.T) {
	registerAllEvents.Do(registerEvents)

	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}

	var s struct {
		OneOf []struct {
			Properties struct {
				Type    struct{ Const bus.Type }
				Subtype struct{ Const bus.Subtype }
			}
		}
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}

	have := map[bus.Key]bool{}
	for _, v := range s.OneOf {
		have[bus.Key{Type: v.Properties.Type.Const, Subtype: v.Properties.Subtype.Const}] = true
	}
	for _, k := range bus.EventKeys() {
		if !have[k] {
			t.Errorf("%s / %s is registered but missing from embed/event.schema.json", k.Type, k.Subtype)
		}
	}
	if len(have) != len(bus.EventKeys()) {
		t.Errorf("the schema holds %d events, %d are registered", len(have), len(bus.EventKeys()))
	}
}

// Every fact kind the Task Manager, the Commit Handler and the Cleanup Handler report has a slot in the event history.
func TestEveryFactKindHasAnEventSlot(t *testing.T) {
	registerAllEvents.Do(registerEvents)

	kinds := emittedFactKinds(t, "../../internal/task", "../../internal/commit", "../../internal/cleanup")
	if len(kinds) < 30 {
		t.Fatalf("the scan found %d fact kinds; it has stopped reading the emitters", len(kinds))
	}
	for _, k := range kinds {
		if err := engine.FactSlot(k); err != nil {
			t.Errorf("fact %q has no event slot: %v", k, err)
		}
	}
}

// emittedFactKinds lists the fact names that the non-test sources of the given folders report, sorted and without repeats.
func emittedFactKinds(t *testing.T, dirs ...string) []string {
	t.Helper()
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			for _, f := range pkg.Files {
				ast.Inspect(f, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						if factCallee(x.Fun) {
							for _, a := range x.Args[:min(len(x.Args), 3)] {
								if s, ok := stringLit(a); ok && s != "" {
									seen[s] = true
								}
							}
						}
					case *ast.KeyValueExpr:
						if id, ok := x.Key.(*ast.Ident); ok && id.Name == "Subtype" {
							if s, ok := stringLit(x.Value); ok && s != "" {
								seen[s] = true
							}
						}
					}
					return true
				})
			}
		}
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// factCallee reports whether a call names one of the functions that report a fact.
func factCallee(fun ast.Expr) bool {
	name := ""
	switch f := fun.(type) {
	case *ast.Ident:
		name = f.Name
	case *ast.SelectorExpr:
		name = f.Sel.Name
	}
	return name == "fact" || name == "say" || name == "report"
}

// stringLit returns the text of a string literal.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
