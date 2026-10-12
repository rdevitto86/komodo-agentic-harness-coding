package core

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestCache builds a cache with a fake clock that ticks one second per call.
func newTestCache(t *testing.T, root, version string, maxBytes int64) *Cache {
	t.Helper()
	c, err := NewCache(root, version, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick = tick.Add(time.Second)
		return tick
	}
	return c
}

func TestCachePutGetRoundTrip(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	key := c.Key("diff", "base", "head")
	if err := c.Put(NSContext, key, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get(NSContext, key)
	if !ok || string(got) != "payload" {
		t.Fatalf("Get = %q, %v; want payload, true", got, ok)
	}
	if _, ok := c.Get(NSChecks, key); ok {
		t.Fatal("same key in another namespace must miss")
	}
	if _, ok := c.Get(NSContext, c.Key("other")); ok {
		t.Fatal("unknown key must miss")
	}
}

func TestCacheKeyIsDeterministicAndUnambiguous(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	if c.Key("a", "b") != c.Key("a", "b") {
		t.Fatal("same inputs must give the same key")
	}
	if c.Key("ab", "c") == c.Key("a", "bc") {
		t.Fatal("input boundaries must change the key")
	}
	if len(c.Key("a")) != 64 {
		t.Fatalf("key length = %d; want 64 hex chars", len(c.Key("a")))
	}
}

func TestCacheDifferentVersionMisses(t *testing.T) {
	root := t.TempDir()
	v1 := newTestCache(t, root, "v1", 0)
	v2 := newTestCache(t, root, "v2", 0)
	if err := v1.Put(NSSkills, v1.Key("index"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if _, ok := v2.Get(NSSkills, v2.Key("index")); ok {
		t.Fatal("a different binary version must miss")
	}
	if got, ok := v1.Get(NSSkills, v1.Key("index")); !ok || string(got) != "old" {
		t.Fatalf("v1 Get = %q, %v; want old, true", got, ok)
	}
}

func TestCacheCorruptEntryIsDeletedAndMisses(t *testing.T) {
	cases := map[string]func(path string) error{
		"flipped byte": func(p string) error {
			b, _ := os.ReadFile(p)
			b[len(b)-1] ^= 0xff
			return os.WriteFile(p, b, 0o644)
		},
		"truncated": func(p string) error {
			b, _ := os.ReadFile(p)
			return os.WriteFile(p, b[:10], 0o644)
		},
		"empty": func(p string) error { return os.WriteFile(p, nil, 0o644) },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTestCache(t, t.TempDir(), "v1", 0)
			key := c.Key("x")
			if err := c.Put(NSChecks, key, []byte("good bytes")); err != nil {
				t.Fatal(err)
			}
			path, _ := c.entryPath(NSChecks, key)
			if err := corrupt(path); err != nil {
				t.Fatal(err)
			}
			got, ok := c.Get(NSChecks, key)
			if ok || got != nil {
				t.Fatalf("Get = %q, %v; want nil, false", got, ok)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("corrupt entry still on disk: %v", err)
			}
		})
	}
}

func TestCacheRejectsBadNamespaceAndKey(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	if err := c.Put("bogus", c.Key("x"), nil); err == nil {
		t.Fatal("unknown namespace must be refused")
	}
	if err := c.Put(NSSkills, "../../etc/passwd", nil); err == nil {
		t.Fatal("non-hash key must be refused")
	}
	if _, ok := c.Get(NSSkills, "../../etc/passwd"); ok {
		t.Fatal("non-hash key must miss")
	}
}

func TestCacheEvictsLeastRecentlyReadUnderCap(t *testing.T) {
	payload := bytes.Repeat([]byte("p"), 300)
	entry := int64(len(payload)) + entryHeaderLen
	c := newTestCache(t, t.TempDir(), "v1", entry*3+10)
	ka, kb, kc, kd := c.Key("a"), c.Key("b"), c.Key("c"), c.Key("d")
	for _, k := range []string{ka, kb, kc} {
		if err := c.Put(NSContext, k, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := c.Get(NSContext, ka); !ok { // a is now the most recently read
		t.Fatal("a must hit before eviction")
	}
	if err := c.Put(NSContext, kd, payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(NSContext, kb); ok {
		t.Fatal("b was read longest ago and must be evicted")
	}
	for _, k := range []string{ka, kc, kd} {
		if _, ok := c.Get(NSContext, k); !ok {
			t.Fatalf("key %s must survive eviction", k[:6])
		}
	}
	st, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Bytes > entry*3+10 || st.Entries != 3 {
		t.Fatalf("stats = %+v; want 3 entries under the cap", st)
	}
}

func TestCacheEvictionAcrossProcessesStaysUnderCap(t *testing.T) {
	root := t.TempDir()
	payload := bytes.Repeat([]byte("p"), 100)
	capBytes := (int64(len(payload)) + entryHeaderLen) * 4
	a := newTestCache(t, root, "v1", capBytes)
	b := newTestCache(t, root, "v1", capBytes)
	for i := 0; i < 10; i++ {
		c := a
		if i%2 == 1 {
			c = b
		}
		if err := c.Put(NSReview, c.Key(string(rune('a'+i))), payload); err != nil {
			t.Fatal(err)
		}
	}
	st, err := a.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Bytes > capBytes {
		t.Fatalf("bytes = %d over cap %d", st.Bytes, capBytes)
	}
}

func TestCachePutRefusesEntryOverCap(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 100)
	err := c.Put(NSMap, c.Key("big"), bytes.Repeat([]byte("x"), 200))
	if !errors.Is(err, ErrCacheEntryTooLarge) {
		t.Fatalf("err = %v; want ErrCacheEntryTooLarge", err)
	}
}

func TestCacheStatsAndClear(t *testing.T) {
	root := t.TempDir()
	c := newTestCache(t, root, "v1", 0)
	if err := c.Put(NSSkills, c.Key("1"), []byte("aaaa")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(NSSkills, c.Key("2"), []byte("bb")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(NSSetup, c.Key("3"), []byte("c")); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	want := int64(4+2+1) + 3*entryHeaderLen
	if st.Entries != 3 || st.Bytes != want {
		t.Fatalf("stats = %+v; want 3 entries, %d bytes", st, want)
	}
	if st.Namespaces[NSSkills].Entries != 2 || st.Namespaces[NSSetup].Entries != 1 {
		t.Fatalf("per-namespace stats = %+v", st.Namespaces)
	}

	other := newTestCache(t, t.TempDir(), "v1", 0)
	if err := other.Put(NSChecks, other.Key("z"), []byte("zz")); err != nil {
		t.Fatal(err)
	}
	ost, _ := other.Stats()
	sum := SumCacheStats(st, ost)
	if sum.Entries != 4 || sum.Bytes != st.Bytes+ost.Bytes {
		t.Fatalf("sum = %+v", sum)
	}

	removed, err := c.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if removed.Entries != 3 || removed.Bytes != want {
		t.Fatalf("removed = %+v; want 3 entries, %d bytes", removed, want)
	}
	if st, _ = c.Stats(); st.Entries != 0 || st.Bytes != 0 {
		t.Fatalf("stats after clear = %+v; want empty", st)
	}
	if _, ok := c.Get(NSSkills, c.Key("1")); ok {
		t.Fatal("cleared entry must miss")
	}
	if err := c.Put(NSSkills, c.Key("1"), []byte("again")); err != nil {
		t.Fatalf("put after clear: %v", err)
	}
}

func TestCacheStatsOnMissingRoot(t *testing.T) {
	st, err := CacheStatsOf(filepath.Join(t.TempDir(), "nope"))
	if err != nil || st.Entries != 0 {
		t.Fatalf("stats = %+v, %v; want empty and nil", st, err)
	}
}

func TestCacheConcurrentWritersLeaveValidEntry(t *testing.T) {
	root := t.TempDir()
	payloads := [][]byte{bytes.Repeat([]byte("A"), 50_000), bytes.Repeat([]byte("B"), 70_000)}
	key := newTestCache(t, root, "v1", 0).Key("same")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newTestCache(t, root, "v1", 0) // a separate cache stands in for a separate process
			for j := 0; j < 20; j++ {
				if err := c.Put(NSReview, key, payloads[(i+j)%2]); err != nil {
					t.Error(err)
					return
				}
				if got, ok := c.Get(NSReview, key); ok && !bytes.Equal(got, payloads[0]) && !bytes.Equal(got, payloads[1]) {
					t.Error("read a payload nobody wrote")
					return
				}
			}
		}(i)
	}
	wg.Wait()

	c := newTestCache(t, root, "v1", 0)
	got, ok := c.Get(NSReview, key)
	if !ok || (!bytes.Equal(got, payloads[0]) && !bytes.Equal(got, payloads[1])) {
		t.Fatalf("final entry invalid: ok=%v len=%d", ok, len(got))
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".tmp-") {
			t.Errorf("stray temp file %s", p)
		}
		return nil
	})
}

// testClock is a settable clock that also ticks one second per read.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

// now returns the clock time and ticks it one second.
func (k *testClock) now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.t = k.t.Add(time.Second)
	return k.t
}

// advance moves the clock forward by d.
func (k *testClock) advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.t = k.t.Add(d)
}

func TestCacheRescansDiskAfterInterval(t *testing.T) {
	root := t.TempDir()
	payload := bytes.Repeat([]byte("p"), 100)
	entry := int64(len(payload)) + entryHeaderLen
	capBytes := entry * 4
	clock := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	a := newTestCache(t, root, "v1", capBytes)
	b := newTestCache(t, root, "v1", capBytes)
	a.now, b.now = clock.now, clock.now
	put := func(c *Cache, name string) {
		t.Helper()
		if err := c.Put(NSReview, c.Key(name), payload); err != nil {
			t.Fatal(err)
		}
	}
	put(a, "a1")
	for _, n := range []string{"b1", "b2", "b3"} {
		put(b, n)
	}
	// Each process stays under the cap on its own count while the disk grows past it.
	put(a, "a2")
	put(a, "a3")
	if st, _ := a.Stats(); st.Bytes <= capBytes {
		t.Fatalf("setup: disk holds %d bytes; want it over cap %d", st.Bytes, capBytes)
	}
	clock.advance(cacheRescanInterval + time.Second)
	put(a, "a4")
	st, err := a.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Bytes > capBytes {
		t.Fatalf("bytes = %d after the rescan interval; want <= cap %d", st.Bytes, capBytes)
	}
	if _, ok := a.Get(NSReview, a.Key("a4")); !ok {
		t.Fatal("the entry just written must survive eviction")
	}
}

func TestCacheScanFailureSurfacesAndRetries(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the current user cannot read")
	}
	root := t.TempDir()
	c := newTestCache(t, root, "v1", 0)
	bad := filepath.Join(root, string(NSReview))
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })
	payload := []byte("payload")
	size := int64(len(payload)) + entryHeaderLen
	if err := c.Put(NSContext, c.Key("one"), payload); err == nil {
		t.Fatal("Put = nil; want the scan error")
	}
	c.mu.Lock()
	unloaded := c.index == nil
	c.mu.Unlock()
	if !unloaded {
		t.Fatal("index must stay unloaded after a failed scan so the next call retries")
	}
	if err := os.Chmod(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(NSContext, c.Key("two"), payload); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.index == nil || c.total != 2*size {
		t.Fatalf("index total = %d; want %d from both entries", c.total, 2*size)
	}
}

func TestCachePutKeepsExistingEntryWhenRenameFails(t *testing.T) {
	root := t.TempDir()
	c := newTestCache(t, root, "v1", 0)
	key := c.Key("same inputs")
	if err := c.Put(NSChecks, key, []byte("result")); err != nil {
		t.Fatal(err)
	}
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	renameFile = func(string, string) error { return errors.New("access is denied") }

	if err := c.Put(NSChecks, key, []byte("result")); err != nil {
		t.Fatalf("Put = %v; want nil because a valid entry already exists", err)
	}
	if got, ok := c.Get(NSChecks, key); !ok || string(got) != "result" {
		t.Fatalf("Get = %q, %v; want the existing entry", got, ok)
	}
	path, _ := c.entryPath(NSChecks, key)
	for _, n := range dirNames(t, filepath.Dir(path)) {
		if strings.Contains(n, ".tmp-") {
			t.Fatalf("temp file %s left behind", n)
		}
	}

	if err := c.Put(NSChecks, c.Key("new"), []byte("x")); err == nil {
		t.Fatal("Put = nil; want the rename error when no entry exists")
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(NSChecks, key, []byte("result")); err == nil {
		t.Fatal("Put = nil; want the rename error when the existing entry is corrupt")
	}
}
