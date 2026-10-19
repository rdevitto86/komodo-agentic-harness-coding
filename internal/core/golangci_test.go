package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGolangciConfigsReturnsACopy(t *testing.T) {
	got := GolangciConfigs()
	if len(got) != 4 || got[0] != ".golangci.yml" {
		t.Fatalf("got %v", got)
	}

	got[0] = "changed"
	if GolangciConfigs()[0] != ".golangci.yml" {
		t.Fatal("a caller changed the shared list")
	}
}

func TestLookGolangciFollowsPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	if _, err := LookGolangci(); err == nil {
		t.Fatal("found a program on an empty PATH")
	}

	bin := filepath.Join(dir, "golangci-lint")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got, err := LookGolangci(); err != nil || got != bin {
		t.Fatalf("got %q %v, want %q", got, err, bin)
	}
}
