package intake

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// osRename renames a file or folder.
func osRename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

// write puts a draft that passed lint on disk and returns what it wrote.
// The files go into a staging folder first; a new epic is then renamed into place whole, and an amended group is swapped in with its old folder set aside and put back on any failure.
func (h *Handler) write(d PlanDraft, pl *planState, reqEpic int) (PlanResult, error) {
	epics, _ := parseFiles(d.Files)
	if len(epics) != 1 {
		return PlanResult{}, writeError{fmt.Errorf("draft holds %d epics; a plan writes one", len(epics))}
	}
	ep := epics[0]
	base := filepath.Join(h.d.Root, filepath.FromSlash(backlogDir))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return PlanResult{}, writeError{fmt.Errorf("create %s: %w", base, err)}
	}
	stage, err := os.MkdirTemp(base, ".plan-")
	if err != nil {
		return PlanResult{}, writeError{fmt.Errorf("create staging folder: %w", err)}
	}
	defer os.RemoveAll(stage)
	var written []string
	for _, f := range d.Files {
		p := filepath.Join(stage, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return PlanResult{}, writeError{fmt.Errorf("stage %s: %w", f.Path, err)}
		}
		if err := core.WriteFileAtomic(p, []byte(f.Content), 0o644); err != nil {
			return PlanResult{}, writeError{fmt.Errorf("stage %s: %w", f.Path, err)}
		}
		written = append(written, backlogDir+"/"+f.Path)
	}
	slices.Sort(written)
	if err := h.swapIn(base, stage, ep); err != nil {
		return PlanResult{}, writeError{err}
	}
	res := PlanResult{Epic: ep.Number, Written: written, Hashes: map[string]string{}}
	merged := mergeEpics(pl.existing, epics)
	graph, _ := BuildGraph(merged, pl.tracked)
	for _, g := range ep.Groups {
		hash, err := CardHash(h.d.Root, g)
		if err != nil {
			return PlanResult{}, writeError{fmt.Errorf("hash group %s: %w", g.Name, err)}
		}
		res.Hashes[g.Name] = hash
	}
	for _, n := range graph.ordered() {
		if _, ok := res.Hashes[n]; ok {
			res.Groups = append(res.Groups, n)
		}
	}
	return res, nil
}

// swapIn moves the staged epic into place: whole when the epic is new, group by group when it exists.
// On a failure every move already made is undone, so each group stays old or becomes new, never half.
func (h *Handler) swapIn(base, stage string, ep Epic) error {
	name := fmt.Sprintf("epic-%d", ep.Number)
	epicDir := filepath.Join(base, name)
	if _, err := os.Lstat(epicDir); errors.Is(err, fs.ErrNotExist) {
		if err := h.rename(filepath.Join(stage, name), epicDir); err != nil {
			return fmt.Errorf("move %s into place: %w", name, err)
		}
		_ = core.SyncDir(base)
		return nil
	}
	aside, err := os.MkdirTemp(base, ".plan-old-")
	if err != nil {
		return fmt.Errorf("create folder for old groups: %w", err)
	}
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		_ = os.RemoveAll(aside)
	}
	for _, g := range ep.Groups {
		dst := filepath.Join(epicDir, g.Name)
		if _, err := os.Lstat(dst); err == nil {
			old := filepath.Join(aside, name, g.Name)
			if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
				rollback()
				return fmt.Errorf("set aside %s: %w", g.Name, err)
			}
			if err := h.rename(dst, old); err != nil {
				rollback()
				return fmt.Errorf("set aside %s: %w", g.Name, err)
			}
			undo = append(undo, func() { _ = h.rename(old, dst) })
		}
		if err := h.rename(filepath.Join(stage, name, g.Name), dst); err != nil {
			rollback()
			return fmt.Errorf("move %s into place: %w", g.Name, err)
		}
		undo = append(undo, func() { _ = os.RemoveAll(dst) })
	}
	_ = os.RemoveAll(aside)
	_ = core.SyncDir(epicDir)
	return nil
}

// restoreOrphans puts back each group that a crash left in a .plan-old-* folder with its place empty, and drops the copies whose place is filled.
func restoreOrphans(root string) error {
	base := filepath.Join(root, filepath.FromSlash(backlogDir))
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".plan-old-") {
			continue
		}
		aside := filepath.Join(base, e.Name())
		if err := restoreAside(base, aside); err != nil {
			return err
		}
		if err := os.RemoveAll(aside); err != nil {
			return err
		}
	}
	return nil
}

// restoreAside moves every group folder of one .plan-old-* folder back to its place when that place is empty.
func restoreAside(base, aside string) error {
	epicDirs, err := os.ReadDir(aside)
	if err != nil {
		return err
	}
	for _, ed := range epicDirs {
		groups, err := os.ReadDir(filepath.Join(aside, ed.Name()))
		if err != nil {
			return err
		}
		for _, g := range groups {
			dst := filepath.Join(base, ed.Name(), g.Name())
			if _, err := os.Lstat(dst); err == nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Rename(filepath.Join(aside, ed.Name(), g.Name()), dst); err != nil {
				return err
			}
		}
	}
	return nil
}
