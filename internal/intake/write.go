package intake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// write puts a draft that passed lint on disk and returns what it wrote. It gives the plan an id and each group without
// one an id, stages plan.md and blueprint.json in a .plan-<random> folder beside the plan folders, then renames the
// staging folder to the plan id in one move.
func (h *Handler) write(d PlanDraft, pl *planState) (PlanResult, error) {
	b := d.blueprint()
	b.Repo = h.d.Repo

	parent := plansParent(h.d.Home, h.d.Repo)
	if err := checkPlanName(h.d.Repo, "plan"); err != nil {
		return PlanResult{}, writeError{err}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return PlanResult{}, writeError{fmt.Errorf("create %s: %w", parent, err)}
	}

	id, err := NewPlanID(func(id string) bool {
		_, err := os.Lstat(filepath.Join(parent, id))
		return err == nil
	})
	if err != nil {
		return PlanResult{}, writeError{err}
	}

	b.Plan = id
	for i := range b.Groups {
		if b.Groups[i].ID == "" {
			b.Groups[i].ID = string(newID())
		}
	}

	text := Render(b)
	res := PlanResult{Plan: id, Path: filepath.Join(PlanDir(h.d.Home, h.d.Repo, id), "plan.md"), Hashes: map[string]string{}}

	for i, g := range b.Groups {
		hash, err := CardHash(text, g)
		if err != nil {
			return PlanResult{}, writeError{fmt.Errorf("hash group %s: %w", g.Name, err)}
		}

		b.Groups[i].Hash, res.Hashes[g.Name] = hash, hash
	}

	stage, err := os.MkdirTemp(parent, ".plan-")
	if err != nil {
		return PlanResult{}, writeError{fmt.Errorf("create staging folder: %w", err)}
	}

	defer func() { _ = os.RemoveAll(stage) }()

	if err := core.WriteFileAtomic(filepath.Join(stage, "plan.md"), []byte(text), 0o644); err != nil {
		return PlanResult{}, writeError{fmt.Errorf("stage plan.md: %w", err)}
	}
	if err := writeBlueprintIn(stage, b); err != nil {
		return PlanResult{}, writeError{err}
	}
	if err := h.rename(stage, filepath.Dir(res.Path)); err != nil {
		return PlanResult{}, writeError{fmt.Errorf("move %s into place: %w", id, err)}
	}

	_ = core.SyncDir(parent)

	graph, _ := BuildGraph(b, pl.tracked)
	res.Groups = graph.ordered()

	return res, nil
}

// osRename renames a file or folder.
func osRename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

// plansParent returns the folder that holds the plan folders of a repository: <home>/plans/<repo>.
func plansParent(home, repo string) string { return filepath.Join(home, "plans", repo) }

// saveFailed writes a failing draft under <home>/plans/<repo>/.plan-failed-<random>/ and returns that folder, or "" when it
// cannot be written.
func (h *Handler) saveFailed(d PlanDraft) string {
	parent := plansParent(h.d.Home, h.d.Repo)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ""
	}

	dir, err := os.MkdirTemp(parent, ".plan-failed-")
	if err != nil {
		return ""
	}

	for _, f := range d.Files {
		safe := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(f.Path)), "/")
		if safe == "" {
			continue
		}

		p := filepath.Join(dir, filepath.FromSlash(safe))
		if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
			_ = os.WriteFile(p, []byte(f.Content), 0o644)
		}
	}

	return dir
}

// restoreOrphans puts back each plan folder that a crash left as .plan-old-<random> with its place empty, and drops the
// copies whose place is filled. A folder whose plan id cannot be read stays for a person.
func restoreOrphans(home, repo string) error {
	parent := plansParent(home, repo)

	entries, err := os.ReadDir(parent)
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

		aside := filepath.Join(parent, e.Name())

		id := asidePlanID(aside)
		if checkPlanName(repo, id) != nil {
			continue
		}

		dst := filepath.Join(parent, id)
		if _, err := os.Lstat(dst); err == nil {
			if err := os.RemoveAll(aside); err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(aside, dst); err != nil {
			return err
		}
	}

	return nil
}

// asidePlanID reads the plan id of an old plan folder from its blueprint.json, or from plan.md when that is gone.
func asidePlanID(dir string) string {
	if data, err := os.ReadFile(filepath.Join(dir, "blueprint.json")); err == nil {
		var b Blueprint
		if json.Unmarshal(data, &b) == nil && b.Plan != "" {
			return b.Plan
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, "plan.md"))
	if err != nil {
		return ""
	}

	b, _ := Parse(string(data))

	return b.Plan
}
