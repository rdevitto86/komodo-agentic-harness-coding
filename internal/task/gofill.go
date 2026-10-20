package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// goFillTimeout is how long a host `go mod download` may run before it is killed with its process group.
const goFillTimeout = 10 * time.Minute

// hostGoDownload runs `go mod download` in dir on the host with env; a test replaces it.
var hostGoDownload = func(ctx context.Context, dir string, env []string) error {
	_, err := hostOutput(ctx, dir, env, "go mod download", goFillTimeout, "go", "mod", "download")
	return err
}

// fillGoModules fills the person's module cache from the host before a Go repo's instance is made, so the sandbox only
// reads it. It runs once per group and again when go.mod or go.sum changed since the last fill; the worktree is left as
// found.
func (m *Manager) fillGoModules(ctx context.Context, role string, spec sandbox.Spec) error {
	if spec.Modules == "" {
		return nil
	}

	m.fillMu.Lock()
	defer m.fillMu.Unlock()

	sum, ok := goModSum(spec.Worktree)
	if !ok || m.filled == sum {
		return nil
	}

	env := append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	saved := saveModFiles(spec.Worktree)

	err := hostGoDownload(ctx, spec.Worktree, env)
	if rerr := saved.restore(); err == nil {
		err = rerr
	}
	if err != nil {
		return block("setup", map[string]string{
			"role":   role,
			"output": agent.CapOutput(err.Error(), agent.GateOutputCap),
			"fix":    m.goModFix(),
		})
	}

	m.filled = sum
	return nil
}

// modFiles holds go.mod and go.sum as a fill found them.
type modFiles []modFile

// modFile is one file of a worktree before a fill: its bytes and mode, or its absence.
type modFile struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

// saveModFiles reads go.mod and go.sum at the root of dir.
func saveModFiles(dir string) modFiles {
	var files modFiles

	for _, name := range []string{"go.mod", "go.sum"} {
		f := modFile{path: filepath.Join(dir, name)}
		if info, err := os.Stat(f.path); err == nil {
			f.data, _ = os.ReadFile(f.path)
			f.mode, f.exists = info.Mode().Perm(), true
		}

		files = append(files, f)
	}

	return files
}

// restore writes every saved file back as it was and removes one that did not exist.
func (files modFiles) restore() error {
	var errs []error

	for _, f := range files {
		if !f.exists {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}

		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			errs = append(errs, err)
		} else if err := os.Chmod(f.path, f.mode); err != nil {
			errs = append(errs, err)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("restore go.mod and go.sum: %w", err)
	}

	return nil
}

// goModFix returns the line that tells the person how to fill the module cache by hand.
func (m *Manager) goModFix() string {
	return "run `go mod download` in the repo on this machine, then `komodo run " + m.spec.Group + "`"
}

// goModSum returns a hash of the go.mod and go.sum at the root of dir, and false when dir holds no go.mod.
func goModSum(dir string) (string, bool) {
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", false
	}

	sum, err := os.ReadFile(filepath.Join(dir, "go.sum"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}

	h := sha256.New()
	h.Write(mod)
	h.Write([]byte{0})
	h.Write(sum)

	return hex.EncodeToString(h.Sum(nil)), true
}
