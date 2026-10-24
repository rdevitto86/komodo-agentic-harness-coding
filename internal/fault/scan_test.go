package fault

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBareGoStatementDetector checks the scanner flags a bare go statement and accepts a deferred Recover.
func TestBareGoStatementDetector(t *testing.T) {
	bare := "package x\nfunc f() { go g() }\nfunc g() {}\n"
	if got := bareGoStatements(t, "bare.go", bare); len(got) != 1 {
		t.Fatalf("bare go not flagged: %v", got)
	}
	lit := "package x\nfunc f() { go func() { work() }() }\nfunc work() {}\n"
	if got := bareGoStatements(t, "lit.go", lit); len(got) != 1 {
		t.Fatalf("go func without Recover not flagged: %v", got)
	}
	nested := "package x\nfunc f(h H) { go func() { func() { defer h.Recover(s, nil) }(); work() }() }\nfunc work() {}\n"
	if got := bareGoStatements(t, "nested.go", nested); len(got) != 1 {
		t.Fatalf("Recover in a nested literal not flagged: %v", got)
	}
	late := "package x\nfunc f(h H) { go func() { work(); defer h.Recover(s, nil) }() }\nfunc work() {}\n"
	if got := bareGoStatements(t, "late.go", late); len(got) != 1 {
		t.Fatalf("Recover that is not the first statement not flagged: %v", got)
	}
	ok := "package x\nfunc f(h H) { go func() { defer h.Recover(s, nil); work() }() }\nfunc work() {}\n"
	if got := bareGoStatements(t, "ok.go", ok); len(got) != 0 {
		t.Fatalf("deferred Recover flagged: %v", got)
	}
}

// TestNoBareGoStatements: no go statement outside fault starts without Go or a deferred Recover.
func TestNoBareGoStatements(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	var bare []string
	for _, dir := range []string{"cmd", "internal"} {
		start := filepath.Join(root, dir)
		if _, err := os.Stat(start); err != nil {
			continue
		}
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && p == filepath.Join(root, "internal", "fault") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			bare = append(bare, bareGoStatements(t, p, nil)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bare) > 0 {
		t.Fatalf("go statements without Go or a deferred Recover:\n%s", strings.Join(bare, "\n"))
	}
}

// bareGoStatements returns the position of every go statement in src that does not defer a Recover first.
func bareGoStatements(t *testing.T, name string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var bare []string
	ast.Inspect(f, func(n ast.Node) bool {
		g, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		if lit, ok := g.Call.Fun.(*ast.FuncLit); !ok || !defersRecover(lit.Body) {
			bare = append(bare, fset.Position(g.Pos()).String())
		}
		return true
	})
	return bare
}

// defersRecover reports whether the first statement of body defers a call to a method named Recover.
func defersRecover(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	d, ok := body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	sel, ok := d.Call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Recover"
}
