package core

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := WriteFileAtomic(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Fatalf("read = %q, %v; want two", got, err)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Fatalf("dir holds %v; want only a.txt", names)
	}
}

func TestWriteAtomicFailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	boom := errors.New("disk full")

	// A failed first write leaves neither the target nor a temp file.
	err := WriteAtomic(path, 0o644, func(w io.Writer) error {
		if _, werr := w.Write(bytes.Repeat([]byte("x"), 1024)); werr != nil {
			return werr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want %v", err, boom)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("dir holds %v after failed write; want empty", names)
	}

	// A failed overwrite keeps the old content whole.
	if err := WriteFileAtomic(path, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = WriteAtomic(path, 0o644, func(w io.Writer) error {
		_, _ = w.Write([]byte("half"))
		return boom
	})
	got, _ := os.ReadFile(path)
	if string(got) != "good" {
		t.Fatalf("content = %q after failed overwrite; want good", got)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Fatalf("dir holds %v; want only a.txt", names)
	}
}

func TestWriteFileAtomicRenameFailureCleansTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x"), 0o644); err == nil {
		t.Fatal("expected rename onto a non-empty directory to fail")
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "target" {
		t.Fatalf("dir holds %v; want only target", names)
	}
}

// TestSyncDirIgnoresWrappedEINVAL covers filesystems that cannot sync a directory and report EINVAL inside a PathError.
func TestSyncDirIgnoresWrappedEINVAL(t *testing.T) {
	dir := t.TempDir()
	orig := syncDirHandle
	t.Cleanup(func() { syncDirHandle = orig })
	syncDirHandle = func(*os.File) error {
		return &fs.PathError{Op: "sync", Path: dir, Err: syscall.EINVAL}
	}
	if err := SyncDir(dir); err != nil {
		t.Fatalf("syncDir = %v; want nil for a wrapped EINVAL", err)
	}
	target := filepath.Join(dir, "target")
	if err := WriteFileAtomic(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic = %v; want nil after the rename landed", err)
	}
	boom := errors.New("disk on fire")
	syncDirHandle = func(*os.File) error { return &fs.PathError{Op: "sync", Path: dir, Err: boom} }
	if err := SyncDir(dir); !errors.Is(err, boom) {
		t.Fatalf("syncDir = %v; want the other error returned", err)
	}
}

// TestWriteAtomicRenameFailureIsTyped checks a failed rename is reported as a renameError and leaves no temp file.
func TestWriteAtomicRenameFailureIsTyped(t *testing.T) {
	dir := t.TempDir()
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	denied := errors.New("access is denied")
	renameFile = func(string, string) error { return denied }
	err := WriteFileAtomic(filepath.Join(dir, "target"), []byte("x"), 0o644)
	var re *renameError
	if !errors.As(err, &re) || !errors.Is(err, denied) {
		t.Fatalf("err = %v; want a renameError wrapping the rename failure", err)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("dir holds %v; want it empty", names)
	}
}

// dirNames lists the entry names in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}
