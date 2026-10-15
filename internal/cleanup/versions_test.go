package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnusedVersionsExcludesVersionsOfUnfinishedGroups(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"1.0.0-beta.4", "1.0.0-beta.5", "1.0.0-beta.10", "1.0.0"} {
		e.write(filepath.Join(e.roots.Versions, v, "komodo"), "bin")
	}
	e.write(filepath.Join(e.roots.Versions, "stray.txt"), "x")
	e.groups.refs = []GroupRef{{Group: "blocked-one", Version: "1.0.0-beta.5"}, {Group: "g2", Version: "9.9.9"}}
	got, err := e.h.UnusedVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.0.0-beta.4", "1.0.0-beta.10", "1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(e.roots.Versions, "1.0.0-beta.4")); err != nil {
		t.Fatal("UnusedVersions deleted a folder")
	}
}

func TestUnusedVersionsWithNoInstallFolder(t *testing.T) {
	e := newEnv(t)
	e.h.Roots.Versions = filepath.Join(e.base, "missing")
	got, err := e.h.UnusedVersions(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}
