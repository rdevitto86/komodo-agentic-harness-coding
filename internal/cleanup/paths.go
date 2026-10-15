package cleanup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validName reports whether name is one plain path element, such as a group id.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}

// inside returns nil when path is strictly below root, by its text and after resolving the links of its parent folder.
// Anything else, including the root itself and a relative path, is ErrOutsideRoots.
func inside(root, path string) error {
	if root == "" || path == "" || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return fmt.Errorf("%q in %q: %w", path, root, ErrOutsideRoots)
	}
	cr, cp := filepath.Clean(root), filepath.Clean(path)
	rel, err := filepath.Rel(cr, cp)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q in %q: %w", path, root, ErrOutsideRoots)
	}
	realRoot, err := filepath.EvalSymlinks(cr)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing exists below a missing root
	}
	if err != nil {
		return fmt.Errorf("resolve %s: %w", root, err)
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(cp))
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing exists below a missing parent
	}
	if err != nil {
		return fmt.Errorf("resolve %s: %w", filepath.Dir(cp), err)
	}
	if realParent != realRoot && !strings.HasPrefix(realParent, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("%q in %q: %w", path, root, ErrOutsideRoots)
	}
	return nil
}

// child returns root/name after checking that name is one plain element and the result stays inside root.
func child(root, name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("name %q: %w", name, ErrOutsideRoots)
	}
	p := filepath.Join(root, name)
	if err := inside(root, p); err != nil {
		return "", err
	}
	return p, nil
}

// removeInside deletes path and everything below it, but only when path is inside root; a link is unlinked, never followed.
// It reports whether anything was there to remove.
func removeInside(root, path string) (bool, error) {
	if err := inside(root, path); err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.RemoveAll(path); err != nil {
		return false, fmt.Errorf("remove %s: %w", path, err)
	}
	return true, nil
}

// exists reports whether anything, even a broken link, sits at path.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
