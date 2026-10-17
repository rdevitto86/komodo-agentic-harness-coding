package task

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// resolved is a path the guard accepted: Rel is relative to the real worktree root with slashes,
// Abs is the real path on disk.
type resolved struct {
	Rel  string
	Abs  string
	Root string // the real worktree root the path was resolved under
}

// resolveInside resolves p against the worktree root with every symlink followed and returns why
// when it leaves the root. A link that cannot be followed, including a dangling one, counts as
// leaving it.
func resolveInside(root, p string) (resolved, string) {
	if p == "" {
		return resolved{}, "is empty"
	}

	if strings.ContainsRune(p, 0) {
		return resolved{}, "contains a NUL byte"
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return resolved{}, "cannot be checked because the worktree cannot be resolved"
	}

	abs := filepath.Join(realRoot, p)
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
		if rest, ok := strings.CutPrefix(abs, filepath.Clean(root)+string(filepath.Separator)); ok {
			abs = filepath.Join(realRoot, rest)
		}
	}

	real, err := evalExisting(abs)
	if err != nil {
		return resolved{}, "cannot be followed to a real location"
	}

	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		if inside(realRoot, abs) {
			return resolved{}, "resolves outside the worktree through a symlink"
		}

		return resolved{}, "is outside the worktree"
	}

	rel, err := filepath.Rel(realRoot, real)
	if err != nil {
		return resolved{}, "is outside the worktree"
	}

	return resolved{Rel: filepath.ToSlash(rel), Abs: real, Root: realRoot}, ""
}

// inside reports whether abs lies under root by its spelling alone.
func inside(root, abs string) bool {
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// evalExisting follows the symlinks of the deepest part of abs that exists and appends the rest,
// which cannot be a link. A part that exists but cannot be followed, such as a dangling link, is an
// error.
func evalExisting(abs string) (string, error) {
	cur, rest := abs, ""
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(real, rest), nil
		}

		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}

		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", err
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}

		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// matchAny reports whether a path matches the pattern list: some positive pattern matches and no
// "!" pattern does. A home-anchored (absolute) pattern is tested against the real path, and never
// for a path inside the worktree.
func matchAny(pats Patterns, rel, abs string, inWorktree, fold bool) bool {
	hit := false

	for _, p := range pats {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		cand := rel

		if path.IsAbs(p) {
			if inWorktree {
				continue
			}

			cand = filepath.ToSlash(abs)
		}

		if fold {
			p, cand = strings.ToLower(p), strings.ToLower(cand)
		}

		if !agent.MatchGlob(p, cand) {
			continue
		}

		if neg {
			return false
		}

		hit = true
	}

	return hit
}

// secretFiles lists the absolute paths of the regular files under the worktree whose names match
// the secret list.
func secretFiles(worktree string, pats Patterns) []string {
	realRoot, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return nil
	}

	var out []string

	_ = filepath.WalkDir(realRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}

			return nil
		}

		rel, rerr := filepath.Rel(realRoot, p)
		if rerr == nil && d.Type().IsRegular() &&
			matchAny(pats, filepath.ToSlash(rel), p, true, true) {
			out = append(out, filepath.Join(worktree, rel))
		}

		return nil
	})

	return out
}
