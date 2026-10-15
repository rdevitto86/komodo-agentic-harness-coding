package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// DefaultLangMaxBytes is the language store cap used when none is given (10 GB).
const DefaultLangMaxBytes int64 = 10 << 30

// langFile is one file of a language store as seen by a trim.
type langFile struct {
	path string
	size int64
	used int64 // modification time in nanoseconds
}

// TrimLang removes the least recently modified files of the language store at dir until it holds about 80% of its cap.
// It returns the bytes removed and never removes dir itself; a store under its cap or a missing dir removes nothing.
func (c *Cache) TrimLang(dir string) (int64, error) {
	var files []langFile
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // vanished between listing and stat
		}
		files = append(files, langFile{path: path, size: info.Size(), used: info.ModTime().UnixNano()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scan language store: %w", err)
	}
	if total <= c.langMaxBytes {
		return 0, nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].used != files[j].used {
			return files[i].used < files[j].used
		}
		return files[i].path < files[j].path
	})
	target := c.langMaxBytes - c.langMaxBytes/5
	var removed int64
	for _, f := range files {
		if total-removed <= target {
			break
		}
		if err := removeLangFile(f.path); err != nil {
			return removed, fmt.Errorf("trim language store: %w", err)
		}
		removed += f.size
		pruneEmptyDirs(filepath.Dir(f.path), dir)
	}
	return removed, nil
}

// removeLangFile deletes a file, first making its folder writable when a dependency cache left it read-only.
func removeLangFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrPermission) {
		if cerr := os.Chmod(filepath.Dir(path), 0o755); cerr == nil {
			err = os.Remove(path)
		}
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// pruneEmptyDirs removes empty folders from dir upward, stopping before stop, which it never removes.
func pruneEmptyDirs(dir, stop string) {
	for dir != stop && len(dir) > len(stop) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
