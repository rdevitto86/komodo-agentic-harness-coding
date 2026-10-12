package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

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
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
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
	if err := d.Sync(); err != nil && err != syscall.EINVAL {
		return err
	}
	return nil
}
