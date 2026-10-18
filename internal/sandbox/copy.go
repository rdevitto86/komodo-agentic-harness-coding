package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Plan says how one language's dependencies reach the Tester's tree.
type Plan struct {
	Language   string
	Copy       []string // folders to copy from the Builder's tree, relative to the worktree
	Setup      []string // a command to run in the Tester's sandbox instead of copying
	Env        []string // variables the Tester's commands need, as NAME=value
	RerunSetup bool     // true when the group's own setup command must run because copying is not safe
}

// PlanOptions carry what a plan depends on besides the repository's files.
type PlanOptions struct {
	LockChanged bool   // the lockfile differs from the one the Builder's dependencies were installed from
	CargoTarget string // the one Cargo target folder the group shares, under the language cache
}

// languageMarkers are the files that show a language, in a stable order.
var languageMarkers = []struct {
	name  string
	files []string
}{
	{"go", []string{"go.mod"}},
	{"node", []string{"package.json"}},
	{"python", []string{"pyproject.toml", "uv.lock", "requirements.txt", "setup.py"}},
	{"rust", []string{"Cargo.toml"}},
}

// PlanDeps returns one plan per language: Go copies nothing, Node copies node_modules, Rust shares one target folder, Python
// and Makefile rerun their setup.
func PlanDeps(dir string, o PlanOptions) []Plan {
	var plans []Plan

	for _, lang := range DetectLanguages(dir) {
		p := Plan{Language: lang}
		switch lang {
		case "node":
			if o.LockChanged {
				p.RerunSetup = true
			} else if _, err := os.Stat(filepath.Join(dir, "node_modules")); err == nil {
				p.Copy = []string{"node_modules"}
			}
		case "rust":
			if o.CargoTarget != "" {
				p.Env = []string{"CARGO_TARGET_DIR=" + o.CargoTarget}
			}
		case "python":
			p.Setup = []string{"uv", "sync"}
		case "make":
			p.Setup = []string{"make", "setup"}
		}

		plans = append(plans, p)
	}

	return plans
}

// DetectLanguages returns the repository's languages, or "make" alone when only a Makefile is there.
func DetectLanguages(dir string) []string {
	if langs := codeLanguages(dir); len(langs) > 0 {
		return langs
	}
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
		return []string{"make"}
	}
	return nil
}

// CopyDeps copies the listed folders from one tree to another with the platform's cheapest copy, keeps symbolic links as
// links and drops those that leave the new tree. A folder missing from the
// source is skipped; a destination that exists, a path outside the tree and a .venv are refused.
func CopyDeps(ctx context.Context, from, to string, rel []string, logf func(format string, args ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	for _, r := range rel {
		clean := filepath.Clean(r)
		if filepath.IsAbs(r) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("copy path %q is outside the worktree", r)
		}
		if slices.Contains(strings.Split(filepath.ToSlash(clean), "/"), ".venv") {
			return fmt.Errorf("copy path %q is a .venv, which is never copied", r)
		}

		src, dst := filepath.Join(from, clean), filepath.Join(to, clean)
		if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("copy destination %s already exists", dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("make the destination folder: %w", err)
		}
		if err := copyOne(ctx, src, dst); err != nil {
			return err
		}
		if err := dropEscapingLinks(to, dst, logf); err != nil {
			return err
		}
	}

	return nil
}

// codeLanguages returns the languages the folder's marker files show, not counting a Makefile.
func codeLanguages(dir string) []string {
	var out []string
	for _, l := range languageMarkers {
		for _, f := range l.files {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				out = append(out, l.name)
				break
			}
		}
	}

	return out
}

// copyOne copies one tree: clonefile or reflink first, a plain copy when the file system refuses, and the byte copy where
// there is no cp.
func copyOne(ctx context.Context, src, dst string) error {
	for _, clone := range []bool{true, false} {
		argv := copyCommand(runtime.GOOS, clone, src, dst)
		if argv == nil {
			return copyTree(src, dst)
		}

		var stderr bytes.Buffer

		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			return nil
		}

		_ = os.RemoveAll(dst)
		if ctx.Err() != nil || !clone || runtime.GOOS != "darwin" {
			return fmt.Errorf("copy %s: %s", src, firstLine(stderr.String(), err))
		}
	}

	return nil
}

// copyCommand returns the cp command line for a platform, or nil where the byte copy is used. Symbolic links are never
// followed; clone asks for clonefile on macOS.
func copyCommand(goos string, clone bool, src, dst string) []string {
	switch goos {
	case "darwin":
		if clone {
			return []string{"cp", "-cRP", src, dst}
		}
		return []string{"cp", "-RP", src, dst}
	case "linux":
		return []string{"cp", "-RP", "--reflink=auto", src, dst}
	}

	return nil
}

// copyTree copies a tree byte by byte; symbolic links stay links.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		}

		return nil
	})
}

// copyFile copies the bytes of one file.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}

	return out.Close()
}

// dropEscapingLinks removes, inside tree, every symbolic link that resolves outside root and every .venv, and logs each.
func dropEscapingLinks(root, tree string, logf func(string, ...any)) error {
	return filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".venv" {
			logf("dropped %s: a .venv is never copied", p)
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}

		link, err := os.Readlink(p)
		if err != nil {
			return err
		}

		abs := link
		if !filepath.IsAbs(link) {
			abs = filepath.Join(filepath.Dir(p), link)
		}
		if within(root, abs) {
			return nil
		}

		logf("dropped link %s -> %s: it resolves outside the worktree", p, link)
		return os.Remove(p)
	})
}

// within reports whether p lies inside root.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
