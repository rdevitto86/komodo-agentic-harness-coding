package core

import (
	"os"
	"path/filepath"
	"testing"
)

// OpenLineFile creates the folder and the file, and each Append is one complete line on disk at once.
func TestLineFileAppendsWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b.jsonl")
	l, err := OpenLineFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := l.Append([]byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{\"n\":1}\n{\"n\":2}\n" {
		t.Fatalf("file = %q", b)
	}
	if l.Path() != path {
		t.Fatalf("Path = %q", l.Path())
	}
}

// A file that ends mid-line, as a killed process leaves it, gets its line ended before the next one.
func TestLineFileEndsATornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.jsonl")
	if err := os.WriteFile(path, []byte(`{"n":`), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLineFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append([]byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{\"n\":\n{\"n\":2}\n" {
		t.Fatalf("file = %q", b)
	}
}

// Closing twice is harmless and an Append after Close fails.
func TestLineFileClose(t *testing.T) {
	l, err := OpenLineFile(filepath.Join(t.TempDir(), "c.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := l.Append([]byte("x")); err == nil {
		t.Fatal("Append after Close succeeded")
	}
}

// A path whose parent is a file cannot be opened.
func TestLineFileOpenFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "f")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLineFile(filepath.Join(blocker, "x.jsonl")); err == nil {
		t.Fatal("want an error")
	}
}
