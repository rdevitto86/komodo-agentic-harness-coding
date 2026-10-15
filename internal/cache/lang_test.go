package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLang writes a file of n bytes under dir with a modification time age ago.
func writeLang(t *testing.T, dir, name string, n int, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTrimLangCutsToEightyPercentOldestFirst(t *testing.T) {
	c, err := New(t.TempDir(), "v1", Limits{LangMaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "lang", "repo-1")
	oldest := writeLang(t, dir, "mod/a", 300, 4*time.Hour)
	older := writeLang(t, dir, "mod/b", 300, 3*time.Hour)
	newer := writeLang(t, dir, "build/c", 300, 2*time.Hour)
	newest := writeLang(t, dir, "build/d", 300, time.Hour)

	removed, err := c.TrimLang(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 600 {
		t.Fatalf("removed = %d; want 600 so 600 remain, under 800", removed)
	}
	for _, p := range []string{oldest, older} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must be trimmed", p)
		}
	}
	for _, p := range []string{newer, newest} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must stay: %v", p, err)
		}
	}
}

func TestTrimLangLeavesAStoreUnderItsCapAlone(t *testing.T) {
	c, _ := New(t.TempDir(), "v1", Limits{LangMaxBytes: 1000})
	dir := t.TempDir()
	p := writeLang(t, dir, "a", 900, time.Hour)
	removed, err := c.TrimLang(dir)
	if err != nil || removed != 0 {
		t.Fatalf("removed = %d, %v; want 0 for a store under its cap", removed, err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestTrimLangNeverDeletesTheStoreWhole(t *testing.T) {
	c, _ := New(t.TempDir(), "v1", Limits{LangMaxBytes: 100})
	dir := filepath.Join(t.TempDir(), "repo-1")
	writeLang(t, dir, "big", 5000, time.Hour)
	if _, err := c.TrimLang(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the store folder must remain after a trim: %v", err)
	}
}

func TestTrimLangRemovesReadOnlyModuleFiles(t *testing.T) {
	c, _ := New(t.TempDir(), "v1", Limits{LangMaxBytes: 100})
	dir := t.TempDir()
	p := writeLang(t, dir, "pkg/mod/x.go", 500, time.Hour)
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(p), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(p), 0o755) })
	if _, err := c.TrimLang(dir); err != nil {
		t.Fatalf("trim of a read-only module cache: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the read-only file must be trimmed")
	}
}

func TestTrimLangMissingDirIsEmpty(t *testing.T) {
	c, _ := New(t.TempDir(), "v1", Limits{LangMaxBytes: 100})
	if removed, err := c.TrimLang(filepath.Join(t.TempDir(), "nope")); err != nil || removed != 0 {
		t.Fatalf("removed = %d, %v; want 0 and nil", removed, err)
	}
}
