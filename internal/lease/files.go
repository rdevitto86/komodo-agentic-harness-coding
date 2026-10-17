package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// FileRegistry keeps one JSON file and one lock file per lease under a root folder.
type FileRegistry struct {
	root        string
	lockTimeout time.Duration
	// OnRepair, when set, is told the path a damaged fence file was set aside to and why; it runs
	// once per repair.
	OnRepair func(setAside string, cause error)
}

const (
	recordSuffix = ".json" // a lease's record is <group-id>.json

	// holds one repository's grants and revocations still; no group id starts with a dot
	repoLockName = ".repo.lock"
)

// crashHook lets a test end the process at a named stage of a write.
var crashHook = func(string) {}

// NewFileRegistry opens the folder of JSON records and lock files; lockTimeout is the longest a
// writer waits for a lock.
func NewFileRegistry(root string, lockTimeout time.Duration) *FileRegistry {
	if lockTimeout <= 0 {
		lockTimeout = DefaultLockTimeout
	}

	return &FileRegistry{root: root, lockTimeout: lockTimeout}
}

// Create writes a new record at rev 1, or returns ErrExists when one is there.
func (r *FileRegistry) Create(ctx context.Context, l Lease) error {
	return r.mutate(ctx, l.Repo, l.Group, func(cur *Lease) (*Lease, error) {
		if cur != nil {
			return nil, ErrExists
		}

		l.Rev = 1

		return &l, nil
	})
}

// Get reads a record without taking its lock; a write is a rename, so the file is whole.
func (r *FileRegistry) Get(ctx context.Context, repo, group string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}

	_, record, _, err := r.paths(repo, group)
	if err != nil {
		return Lease{}, err
	}

	l, err := readRecord(record)
	if err != nil {
		return Lease{}, err
	}

	if l == nil {
		return Lease{}, ErrNotFound
	}

	return *l, nil
}

// Swap replaces the record when its rev is still rev, and returns the new rev.
func (r *FileRegistry) Swap(ctx context.Context, l Lease, rev int) (int, error) {
	err := r.mutate(ctx, l.Repo, l.Group, func(cur *Lease) (*Lease, error) {
		if err := checkRev(cur, rev); err != nil {
			return nil, err
		}

		l.Rev = rev + 1

		return &l, nil
	})
	if err != nil {
		return 0, err
	}

	return rev + 1, nil
}

// Delete removes the record when its rev is still rev.
func (r *FileRegistry) Delete(ctx context.Context, repo, group string, rev int) error {
	return r.mutate(ctx, repo, group, func(cur *Lease) (*Lease, error) {
		return nil, checkRev(cur, rev)
	})
}

// List reads every record of a repository, ordered by group id; it takes no lock.
func (r *FileRegistry) List(ctx context.Context, repo string) ([]Lease, error) {
	dir, err := r.repoDir(repo)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("list lease folder: %w", err)
	}

	var out []Lease

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, recordSuffix) {
			continue
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		l, err := readRecord(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}

		if l != nil { // a record deleted since the listing began is skipped
			out = append(out, *l)
		}
	}

	return out, nil
}

// LockRepo holds one repository's grant lock, which keeps two grants of a repository from
// interleaving across processes.
func (r *FileRegistry) LockRepo(ctx context.Context, repo string) (func(), error) {
	dir, err := r.repoDir(repo)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lease folder: %w", err)
	}

	return r.lock(ctx, filepath.Join(dir, repoLockName))
}

// repoDir returns the folder of one repository's records; the id is escaped so it names a single
// folder.
func (r *FileRegistry) repoDir(repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("%w: empty repo id", ErrBadKey)
	}

	name := url.PathEscape(repo)
	if strings.HasPrefix(name, ".") {
		name = "%2E" + name[1:]
	}

	return filepath.Join(r.root, name), nil
}

// paths returns a lease's folder, record file and lock file.
func (r *FileRegistry) paths(repo, group string) (dir, record, lock string, err error) {
	if dir, err = r.repoDir(repo); err != nil {
		return "", "", "", err
	}

	if !validGroup(group) {
		return "", "", "", fmt.Errorf("%w: group %q", ErrBadKey, group)
	}

	return dir, filepath.Join(dir, group+recordSuffix), filepath.Join(dir, group+LockSuffix), nil
}

// mutate runs fn on a record under its lock file. fn gets the current record (nil when absent) and
// returns the record to write, or nil to delete it.
func (r *FileRegistry) mutate(
	ctx context.Context,
	repo, group string,
	fn func(cur *Lease) (*Lease, error),
) error {
	dir, record, lock, err := r.paths(repo, group)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create lease folder: %w", err)
	}

	unlock, err := r.lock(ctx, lock)
	if err != nil {
		return err
	}

	defer unlock()

	return writeLocked(dir, record, fn)
}

// validGroup reports whether a group id can name a record file.
func validGroup(group string) bool {
	return group != "" && !strings.HasPrefix(group, ".") && !strings.ContainsAny(group, "/\\\x00")
}

// readRecord reads one record file; the pointer is nil when the file does not exist.
func readRecord(path string) (*Lease, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read lease record: %w", err)
	}

	var l Lease
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("decode lease record %s: %w", filepath.Base(path), err)
	}

	return &l, nil
}

// syncFolder flushes a folder entry to disk; a failure is ignored where the OS cannot sync folders.
func syncFolder(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// writeLocked applies fn to the record while its lock is held and writes or deletes the result.
func writeLocked(dir, record string, fn func(cur *Lease) (*Lease, error)) error {
	const (
		// test stage: the record is written and the lock is not yet released
		crashAfter = "after_rename"

		// test stage: the lock is held and nothing is written yet
		crashLocked = "after_lock"
	)

	crashHook(crashLocked)

	cur, err := readRecord(record)
	if err != nil {
		return err
	}

	next, err := fn(cur)
	if err != nil {
		return err
	}

	if next == nil {
		if err := os.Remove(record); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete lease record: %w", err)
		}

		syncFolder(dir)

		return nil
	}

	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encode lease record: %w", err)
	}

	if err := core.WriteFileAtomic(record, append(b, '\n'), 0o644); err != nil {
		return err
	}

	crashHook(crashAfter)

	return nil
}

// checkRev returns ErrNotFound for an absent record and ErrStaleRev for one whose rev moved on.
func checkRev(cur *Lease, rev int) error {
	if cur == nil {
		return ErrNotFound
	}

	if cur.Rev != rev {
		return ErrStaleRev
	}

	return nil
}
