package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newLangCache opens a cache whose language store lives in a temporary folder with the given cap.
func newLangCache(t *testing.T, capBytes int64) (*Cache, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "lang")
	c, err := New(t.TempDir(), "v1", Limits{LangMaxBytes: capBytes, LangRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c.now = clock.now
	return c, root
}

// writeLang writes a file of n bytes under dir, creating folders as needed.
func writeLang(t *testing.T, dir, name string, n int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// keepNone keeps no repository, so every store is open to deletion.
func keepNone(string) bool { return false }

// exists reports whether path is on disk.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// touchAll stamps the repositories in order, so the first is the least recently used.
func touchAll(t *testing.T, c *Cache, repos ...string) {
	t.Helper()
	for _, r := range repos {
		if err := c.TouchLang(r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrimLangDeletesLeastRecentlyUsedRepositoriesWhole(t *testing.T) {
	c, root := newLangCache(t, 1000)
	for _, r := range []string{"a", "b", "c", "d"} {
		writeLang(t, filepath.Join(root, r), "mod/x", 200)
		writeLang(t, filepath.Join(root, r), "build/y", 100)
	}
	touchAll(t, c, "c", "a", "d", "b") // c is the least recently used

	removed, err := c.TrimLang(root, keepNone)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 600 {
		t.Fatalf("removed = %d; want 600 so 600 remain, under 800", removed)
	}
	for _, r := range []string{"c", "a"} {
		if exists(filepath.Join(root, r)) {
			t.Fatalf("repository %s must be deleted whole", r)
		}
	}
	for _, r := range []string{"d", "b"} {
		for _, f := range []string{"mod/x", "build/y"} {
			if !exists(filepath.Join(root, r, f)) {
				t.Fatalf("repository %s lost %s; a unit goes whole or stays whole", r, f)
			}
		}
	}
}

func TestTrimLangOrderFollowsTheLatestStamp(t *testing.T) {
	c, root := newLangCache(t, 500)
	for _, r := range []string{"a", "b"} {
		writeLang(t, filepath.Join(root, r), "x", 300)
	}
	touchAll(t, c, "a", "b", "a") // b is now the older stamp
	if _, err := c.TrimLang(root, keepNone); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "b")) || !exists(filepath.Join(root, "a", "x")) {
		t.Fatal("the repository stamped longest ago must be the one deleted")
	}
}

func TestTrimLangKeepsARepositoryThatHoldsALease(t *testing.T) {
	c, root := newLangCache(t, 800)
	for _, r := range []string{"leased", "idle", "newer"} {
		writeLang(t, filepath.Join(root, r), "x", 300)
	}
	touchAll(t, c, "leased", "idle", "newer") // leased is the oldest
	keep := func(repo string) bool { return repo == "leased" }
	if _, err := c.TrimLang(root, keep); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, "leased", "x")) {
		t.Fatal("a repository that holds a lease must survive")
	}
	if exists(filepath.Join(root, "idle")) {
		t.Fatal("the oldest repository without a lease must be deleted")
	}
	if !exists(filepath.Join(root, "newer", "x")) {
		t.Fatal("trim must stop once the store fits")
	}
}

func TestTrimLangNeverTouchesFilesOfAKeptRepositoryOverTheCap(t *testing.T) {
	c, root := newLangCache(t, 100)
	p := writeLang(t, filepath.Join(root, "leased"), "pkg/big", 5000)
	touchAll(t, c, "leased")
	removed, err := c.TrimLang(root, func(string) bool { return true })
	if err != nil || removed != 0 {
		t.Fatalf("removed = %d, %v; want nothing removed", removed, err)
	}
	if !exists(p) {
		t.Fatal("no file of a kept repository may be deleted")
	}
}

func TestTrimLangRemovesAReadOnlyGoModuleTreeWhole(t *testing.T) {
	c, root := newLangCache(t, 100)
	unit := filepath.Join(root, "repo-1")
	file := writeLang(t, unit, "go/pkg/mod/github.com/x/y@v1.0.0/a.go", 500)
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		"go/pkg/mod/github.com/x/y@v1.0.0",
		"go/pkg/mod/github.com/x",
		"go/pkg/mod/github.com",
		"go/pkg/mod",
		"go/pkg",
		"go",
	} {
		if err := os.Chmod(filepath.Join(unit, d), 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(unit, "go/pkg/mod/github.com/x/y@v1.0.0"), 0o755) })
	touchAll(t, c, "repo-1")

	removed, err := c.TrimLang(root, keepNone)
	if err != nil {
		t.Fatalf("trim of a read-only module tree: %v", err)
	}
	if exists(unit) {
		t.Fatal("the read-only tree must be removed whole")
	}
	if removed < 500 {
		t.Fatalf("removed = %d; want at least the 500-byte file", removed)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("the store's root folder must remain: %v", err)
	}
}

func TestTrimLangUntouchedRepositoryIsTheOldest(t *testing.T) {
	c, root := newLangCache(t, 500)
	writeLang(t, filepath.Join(root, "never"), "x", 300)
	writeLang(t, filepath.Join(root, "stamped"), "x", 300)
	touchAll(t, c, "stamped")
	if _, err := c.TrimLang(root, keepNone); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "never")) || !exists(filepath.Join(root, "stamped", "x")) {
		t.Fatal("a repository with no stamp must be deleted before a stamped one")
	}
}

func TestTrimLangLeavesAStoreUnderItsCapAlone(t *testing.T) {
	c, root := newLangCache(t, 1000)
	p := writeLang(t, filepath.Join(root, "a"), "x", 900)
	touchAll(t, c, "a")
	removed, err := c.TrimLang(root, keepNone)
	if err != nil || removed != 0 || !exists(p) {
		t.Fatalf("removed = %d, %v; want 0 for a store under its cap", removed, err)
	}
}

func TestTrimLangIgnoresLooseFilesAndCleansTheDirArgument(t *testing.T) {
	c, root := newLangCache(t, 100)
	loose := writeLang(t, root, "loose.txt", 5000)
	writeLang(t, filepath.Join(root, "a"), "x", 500)
	touchAll(t, c, "a")
	if _, err := c.TrimLang(filepath.Join(root, "a", "..")+string(os.PathSeparator)+".", keepNone); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "a")) {
		t.Fatal("the repository must be trimmed through an unclean path")
	}
	if !exists(loose) {
		t.Fatal("a file that is not a repository folder must be left alone")
	}
}

func TestTrimLangMissingOrEmptyDir(t *testing.T) {
	c, root := newLangCache(t, 100)
	if removed, err := c.TrimLang(filepath.Join(root, "nope"), keepNone); err != nil || removed != 0 {
		t.Fatalf("removed = %d, %v; want 0 and nil for a missing folder", removed, err)
	}
	noRoot, err := New(t.TempDir(), "v1", Limits{LangMaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noRoot.TrimLang("", keepNone); err == nil {
		t.Fatal("an empty dir with no language root must be refused, not read as the working folder")
	}
}

func TestTouchLangWritesAStampAndRefusesBadRepositoryIds(t *testing.T) {
	c, root := newLangCache(t, 100)
	if err := c.TouchLang("repo-1"); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(root, "repo-1", ".last-used")
	first, err := os.Stat(stamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TouchLang("repo-1"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.Stat(stamp)
	if !second.ModTime().After(first.ModTime()) {
		t.Fatal("a second touch must move the stamp forward")
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if err := c.TouchLang(bad); err == nil {
			t.Errorf("TouchLang(%q) = nil; want an error", bad)
		}
	}
	noRoot, _ := New(t.TempDir(), "v1", Limits{})
	if err := noRoot.TouchLang("repo-1"); err == nil {
		t.Fatal("TouchLang without a language root must fail")
	}
}

func TestTrimLangWithNoKeepFunctionDeletesNothing(t *testing.T) {
	c, root := newLangCache(t, 100)
	p := writeLang(t, filepath.Join(root, "a"), "x", 500)
	touchAll(t, c, "a")
	removed, err := c.TrimLang(root, nil)
	if err != nil || removed != 0 || !exists(p) {
		t.Fatalf("removed = %d, %v; want nothing deleted when no lease check is given", removed, err)
	}
}
