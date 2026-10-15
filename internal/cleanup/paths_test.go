package cleanup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInsideRefusesEveryEscape(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(base, "root-evil")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "jump")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"the root itself":         root,
		"a parent":                base,
		"a dot-dot path":          filepath.Join(root, "..", "outside"),
		"a sibling with a prefix": filepath.Join(base, "root-evil", "x"),
		"through a symlink":       filepath.Join(root, "jump", "x"),
		"a relative path":         "root/x",
		"an empty path":           "",
	}
	for name, p := range cases {
		if err := inside(root, p); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("%s: err = %v, want ErrOutsideRoots", name, err)
		}
	}
	if err := inside(root, filepath.Join(root, "g1")); err != nil {
		t.Errorf("a child: %v", err)
	}
	if err := inside("", filepath.Join(root, "g1")); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("an empty root: err = %v", err)
	}
}

func TestChildRefusesNamesThatAreNotOneElement(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b", "../x", `a\b`, "/abs"} {
		if _, err := child(root, name); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("child(%q): err = %v, want ErrOutsideRoots", name, err)
		}
	}
	if got, err := child(root, "g1"); err != nil || got != filepath.Join(root, "g1") {
		t.Errorf("child(g1) = %q, %v", got, err)
	}
}

func TestRemoveInsideDeletesOnlyBelowRoot(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "g1", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if gone, err := removeInside(root, filepath.Join(root, "g1")); err != nil || !gone {
		t.Fatalf("remove child: %v, %v", gone, err)
	}
	if gone, err := removeInside(root, filepath.Join(root, "g1")); err != nil || gone {
		t.Fatalf("repeat remove: %v, %v", gone, err)
	}
	if _, err := removeInside(root, filepath.Join(base, "keep")); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("outside: %v", err)
	}
	if !exists(filepath.Join(base, "keep")) || !exists(root) {
		t.Fatal("a folder outside the child was removed")
	}
}
