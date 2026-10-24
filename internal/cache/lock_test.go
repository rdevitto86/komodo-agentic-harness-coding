package cache

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// within runs f in a goroutine and reports whether it returned before d.
func within(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestClearDeduplicatesItsNamespaces(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	if err := c.Put(NSContext, c.Key("k"), []byte("data")); err != nil {
		t.Fatal(err)
	}
	removed, err := c.Clear(NSContext, NSContext)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Entries != 1 || removed.Namespaces[NSContext].Entries != 1 {
		t.Fatalf("removed = %+v; want the one entry counted once", removed)
	}
}

func TestGetMissDoesNotWaitForTheCacheLock(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	c.mu.Lock()
	ok := within(2*time.Second, func() { c.Get(NSContext, c.Key("absent")) })
	c.mu.Unlock()
	if !ok {
		t.Fatal("a missing entry's Get blocked on the cache-wide lock")
	}
}

func TestGetOnAClosedCacheDoesNotWaitForTheCacheLock(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	ok := within(2*time.Second, func() { c.Get(NSContext, c.Key("x")) })
	c.mu.Unlock()
	if !ok {
		t.Fatal("Get on a closed cache blocked on the cache-wide lock")
	}
}

func TestClearScansAndRemovesWithoutTheCacheLock(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	if err := c.Put(NSContext, c.Key("k"), []byte("data")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.root, string(NSContext))
	c.mu.Lock()
	finished := make(chan struct{})
	go func() {
		_, _ = c.Clear(NSContext)
		close(finished)
	}()
	gone := false
	for i := 0; i < 200 && !gone; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			gone = true
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	c.mu.Unlock()
	<-finished
	if !gone {
		t.Fatal("Clear removed nothing while another caller held the cache-wide lock")
	}
}

func TestHandleMethodSetIsKeyGetPut(t *testing.T) {
	want := map[string]bool{"Key": true, "Get": true, "Put": true}
	for name, typ := range map[string]reflect.Type{
		"Handle interface": reflect.TypeOf((*Handle)(nil)).Elem(),
		"handed-out value": reflect.TypeOf(newTestCache(t, t.TempDir(), "v1", 0).Handle()),
	} {
		got := map[string]bool{}
		for i := 0; i < typ.NumMethod(); i++ {
			got[typ.Method(i).Name] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s methods = %v; want exactly Key, Get and Put", name, got)
		}
		for _, banned := range []string{"Clear", "Stats", "Close", "TrimLang", "TouchLang"} {
			if got[banned] {
				t.Errorf("%s exposes %s", name, banned)
			}
		}
	}
}
