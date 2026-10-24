package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanDeletesOnlyStaleTempFiles(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	key := c.Key("x")
	if err := c.Put(NSContext, key, []byte("v")); err != nil {
		t.Fatal(err)
	}
	path, _ := c.entryPath(NSContext, key)
	dir := filepath.Dir(path)
	stale := filepath.Join(dir, "."+string(key)+".json.tmp-111")
	fresh := filepath.Join(dir, "."+string(key)+".json.tmp-222")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	entries, err := scanCache(c.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].path != path {
		t.Fatalf("entries = %+v; want only the real entry", entries)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file still on disk: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp file must stay: %v", err)
	}
}

func TestStatsOfTotalsPerNamespace(t *testing.T) {
	s := statsOf([]cacheEntry{{ns: NSMap, size: 10}, {ns: NSMap, size: 5}, {ns: NSSetup, size: 1}})
	if s.Entries != 3 || s.Bytes != 16 || s.Namespaces[NSMap] != (NamespaceStats{Entries: 2, Bytes: 15}) {
		t.Fatalf("stats = %+v", s)
	}
	if _, ok := s.Namespaces[NSSkills]; ok {
		t.Fatal("a namespace without entries must not appear")
	}
}
