package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTree creates the files (relative path to body) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectLanguages(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"go.mod": "", "package.json": "", "Cargo.toml": "", "uv.lock": "", "Makefile": ""})
	if got, want := DetectLanguages(dir), []string{"go", "node", "python", "rust"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	mk := t.TempDir()
	writeTree(t, mk, map[string]string{"Makefile": ""})
	if got := DetectLanguages(mk); !reflect.DeepEqual(got, []string{"make"}) {
		t.Fatalf("Makefile-only: %v", got)
	}
	if got := DetectLanguages(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

func TestPlanDepsPerLanguage(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"go.mod": "", "package.json": "", "node_modules/a/index.js": "x", "Cargo.toml": "", "pyproject.toml": "", ".venv/bin/python": "x"})
	plans := PlanDeps(dir, PlanOptions{CargoTarget: "/cache/lang/shop/g1-target"})
	by := map[string]Plan{}
	for _, p := range plans {
		by[p.Language] = p
	}
	if len(by) != 4 {
		t.Fatalf("plans %+v", plans)
	}
	if p := by["go"]; len(p.Copy) != 0 || len(p.Setup) != 0 || p.RerunSetup {
		t.Fatalf("go copies nothing: %+v", p)
	}
	if p := by["node"]; !reflect.DeepEqual(p.Copy, []string{"node_modules"}) || p.RerunSetup {
		t.Fatalf("node: %+v", p)
	}
	if p := by["rust"]; len(p.Copy) != 0 || !reflect.DeepEqual(p.Env, []string{"CARGO_TARGET_DIR=/cache/lang/shop/g1-target"}) {
		t.Fatalf("rust shares one target: %+v", p)
	}
	if p := by["python"]; !reflect.DeepEqual(p.Setup, []string{"uv", "sync"}) || len(p.Copy) != 0 {
		t.Fatalf("python reruns uv sync and never copies .venv: %+v", p)
	}
}

func TestPlanDepsMakefileOnlyAndChangedLockfile(t *testing.T) {
	mk := t.TempDir()
	writeTree(t, mk, map[string]string{"Makefile": ""})
	if p := PlanDeps(mk, PlanOptions{}); len(p) != 1 || !reflect.DeepEqual(p[0].Setup, []string{"make", "setup"}) {
		t.Fatalf("make: %+v", p)
	}
	nd := t.TempDir()
	writeTree(t, nd, map[string]string{"package.json": "", "node_modules/a/index.js": "x"})
	p := PlanDeps(nd, PlanOptions{LockChanged: true})
	if len(p) != 1 || len(p[0].Copy) != 0 || !p[0].RerunSetup {
		t.Fatalf("a changed lockfile reruns setup instead of copying: %+v", p)
	}
	none := t.TempDir()
	writeTree(t, none, map[string]string{"package.json": ""})
	if p := PlanDeps(none, PlanOptions{}); len(p) != 1 || len(p[0].Copy) != 0 {
		t.Fatalf("nothing installed, nothing to copy: %+v", p)
	}
}

func TestCopyCommandPerPlatform(t *testing.T) {
	if got := strings.Join(copyCommand("darwin", true, "a", "b"), " "); got != "cp -cRP a b" {
		t.Fatalf("darwin: %q", got)
	}
	if got := strings.Join(copyCommand("darwin", false, "a", "b"), " "); got != "cp -RP a b" {
		t.Fatalf("darwin plain: %q", got)
	}
	if got := strings.Join(copyCommand("linux", true, "a", "b"), " "); got != "cp -RP --reflink=auto a b" {
		t.Fatalf("linux: %q", got)
	}
	if copyCommand("windows", true, "a", "b") != nil {
		t.Fatal("windows has no cp: the byte copy runs")
	}
}

func TestCopyDepsKeepsLinksAndDropsEscapingOnes(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	writeTree(t, from, map[string]string{"node_modules/a/index.js": "code", "node_modules/b/real.js": "real", "outside.txt": "o"})
	nm := filepath.Join(from, "node_modules")
	mustLink := func(target, name string) {
		if err := os.Symlink(target, filepath.Join(nm, name)); err != nil {
			t.Fatal(err)
		}
	}
	mustLink("b/real.js", "inside-link")                         // stays: resolves inside the tree
	mustLink("../../../../../../../../etc/hosts", "escape-link") // dropped: leaves the tree
	mustLink(filepath.Join(from, "outside.txt"), "abs-link")     // dropped: points into the builder's tree
	var logged []string
	logf := func(format string, args ...any) {
		logged = append(logged, format+" "+strings.Join(toStrings(args), " "))
	}
	if err := CopyDeps(context.Background(), from, to, []string{"node_modules"}, logf); err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(to, "node_modules")
	if b, err := os.ReadFile(filepath.Join(got, "a", "index.js")); err != nil || string(b) != "code" {
		t.Fatalf("file not copied: %v %q", err, b)
	}
	if tgt, err := os.Readlink(filepath.Join(got, "inside-link")); err != nil || tgt != "b/real.js" {
		t.Fatalf("a link inside the tree stays a link: %q %v", tgt, err)
	}
	for _, gone := range []string{"escape-link", "abs-link"} {
		if _, err := os.Lstat(filepath.Join(got, gone)); err == nil {
			t.Errorf("%s survived", gone)
		}
	}
	if len(logged) != 2 {
		t.Fatalf("each dropped link is logged: %v", logged)
	}
	// The copy is independent: writing it leaves the builder's tree alone.
	if err := os.WriteFile(filepath.Join(got, "a", "index.js"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(nm, "a", "index.js")); string(b) != "code" {
		t.Fatal("the copy shares bytes with the source")
	}
}

func TestCopyDepsRefusesBadInput(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	writeTree(t, from, map[string]string{"node_modules/a": "x", ".venv/bin/python": "x"})
	ctx := context.Background()
	for _, rel := range []string{"../escape", "/abs", ".venv", "node_modules/../../x"} {
		if err := CopyDeps(ctx, from, to, []string{rel}, nil); err == nil {
			t.Errorf("%q must be refused", rel)
		}
	}
	if err := CopyDeps(ctx, from, to, []string{"node_modules"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := CopyDeps(ctx, from, to, []string{"node_modules"}, nil); err == nil {
		t.Fatal("an existing destination must be refused")
	}
	if err := CopyDeps(ctx, from, to, []string{"missing"}, nil); err != nil {
		t.Fatalf("a folder that is not there is skipped: %v", err)
	}
}

func TestByteCopyKeepsSymlinksAsLinks(t *testing.T) {
	from, to := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	writeTree(t, from, map[string]string{"d/f.txt": "data"})
	if err := os.Symlink("f.txt", filepath.Join(from, "d", "l")); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(from, to); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "d", "f.txt")); string(b) != "data" {
		t.Fatal("file not copied")
	}
	if tgt, err := os.Readlink(filepath.Join(to, "d", "l")); err != nil || tgt != "f.txt" {
		t.Fatalf("link %q %v", tgt, err)
	}
}

// toStrings renders log arguments as text.
func toStrings(args []any) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i], _ = a.(string)
	}
	return out
}
