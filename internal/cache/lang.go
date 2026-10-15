package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultLangMaxBytes is the language store cap used when none is given (10 GB).
const DefaultLangMaxBytes int64 = 10 << 30

// langStamp is the file in a repository's language store whose modification time records its last use.
const langStamp = ".last-used"

// langUnit is one repository's language store as seen by a trim.
type langUnit struct {
	repo string
	path string
	size int64
	used time.Time // the stamp's modification time; zero when the repository was never stamped
}

// TouchLang records that the repository's language store is in use now, creating the store folder if needed.
// The Runtime Manager calls it when a group of that repository starts.
func (c *Cache) TouchLang(repo string) error {
	if c.langRoot == "" {
		return errors.New("language store root is not set")
	}
	if repo == "" || repo == "." || repo == ".." || strings.ContainsAny(repo, `/\`) {
		return fmt.Errorf("repository id %q is not a single folder name", repo)
	}
	dir := filepath.Join(c.langRoot, repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create language store: %w", err)
	}
	stamp := filepath.Join(dir, langStamp)
	if err := os.WriteFile(stamp, nil, 0o644); err != nil {
		return fmt.Errorf("write language store stamp: %w", err)
	}
	now := c.now()
	if err := os.Chtimes(stamp, now, now); err != nil {
		return fmt.Errorf("stamp language store: %w", err)
	}
	return nil
}

// TrimLang deletes whole repository stores under dir, least recently used first, until the language store holds about 80% of its cap.
// A repository for which keep returns true is never deleted; a nil keep keeps all. It returns the bytes removed and never removes dir.
func (c *Cache) TrimLang(dir string, keep func(repo string) bool) (int64, error) {
	if dir == "" {
		dir = c.langRoot
	}
	if dir == "" {
		return 0, errors.New("language store folder is empty")
	}
	dir = filepath.Clean(dir)
	if keep == nil {
		keep = func(string) bool { return true }
	}
	units, total, err := scanLang(dir)
	if err != nil {
		return 0, err
	}
	if total <= c.langMaxBytes {
		return 0, nil
	}
	sort.Slice(units, func(i, j int) bool {
		if !units[i].used.Equal(units[j].used) {
			return units[i].used.Before(units[j].used)
		}
		return units[i].repo < units[j].repo
	})
	target := c.langMaxBytes - c.langMaxBytes/5
	var removed int64
	for _, u := range units {
		if total-removed <= target {
			break
		}
		if keep(u.repo) {
			continue
		}
		if err := removeTree(u.path); err != nil {
			return removed, fmt.Errorf("delete language store of %s: %w", u.repo, err)
		}
		removed += u.size
	}
	return removed, nil
}

// scanLang lists the repository stores directly under dir with their sizes and stamps; a missing dir is empty.
func scanLang(dir string) ([]langUnit, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("read language store: %w", err)
	}
	var units []langUnit
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		u := langUnit{repo: e.Name(), path: filepath.Join(dir, e.Name())}
		if info, err := os.Stat(filepath.Join(u.path, langStamp)); err == nil {
			u.used = info.ModTime()
		}
		err := filepath.WalkDir(u.path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				u.size += info.Size()
			}
			return nil
		})
		if err != nil {
			return nil, 0, fmt.Errorf("scan language store of %s: %w", u.repo, err)
		}
		units = append(units, u)
		total += u.size
	}
	return units, total, nil
}

// removeTree deletes a folder and everything in it, first making read-only folders writable as a module cache leaves them.
func removeTree(root string) error {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o755)
		}
		return nil
	})
	return os.RemoveAll(root)
}
