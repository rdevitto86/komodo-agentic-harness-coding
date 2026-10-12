package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// renameFile moves the temp file into place; tests replace it to simulate rename failures.
var renameFile = os.Rename

// syncDirHandle flushes an open directory; tests replace it to simulate sync failures.
var syncDirHandle = func(d *os.File) error { return d.Sync() }

// renameError marks a WriteAtomic failure at the final rename, after the temp file was written and synced.
type renameError struct {
	path string
	err  error
}

// Error describes the failed rename.
func (e *renameError) Error() string { return fmt.Sprintf("rename onto %s: %v", e.path, e.err) }

// Unwrap returns the underlying rename error.
func (e *renameError) Unwrap() error { return e.err }

// WriteFileAtomic replaces path with data so a reader sees the old file or the new one, never a partial one.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return WriteAtomic(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteAtomic streams fill into a temp file beside path, syncs it, then renames it over path.
// A failure at any step removes the temp file and leaves path untouched.
func WriteAtomic(path string, perm os.FileMode, fill func(w io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err = fill(tmp); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("set mode on %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err = renameFile(tmpName, path); err != nil {
		err = &renameError{path: path, err: err}
		return err
	}
	if err = syncDir(dir); err != nil {
		return fmt.Errorf("sync directory of %s: %w", path, err)
	}
	return nil
}

// syncDir flushes a directory entry to disk; it is a no-op where the OS cannot sync directories.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := syncDirHandle(d); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
