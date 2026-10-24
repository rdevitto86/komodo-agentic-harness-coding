package cache

import (
	"os"
	"reflect"
	"testing"
)

func TestPutSyncsTheTempFileBeforeRenameAndTheFolderAfter(t *testing.T) {
	var order []string
	origSync, origRename, origDir := syncFile, renameFile, syncDirHandle
	t.Cleanup(func() { syncFile, renameFile, syncDirHandle = origSync, origRename, origDir })
	syncFile = func(f *os.File) error { order = append(order, "file"); return f.Sync() }
	renameFile = func(a, b string) error { order = append(order, "rename"); return os.Rename(a, b) }
	syncDirHandle = func(d *os.File) error { order = append(order, "dir"); return d.Sync() }

	c := newTestCache(t, t.TempDir(), "v1", 0)
	if err := c.Put(NSContext, c.Key("x"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"file", "rename", "dir"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v; want %v", order, want)
	}
}
