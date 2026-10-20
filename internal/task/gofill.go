package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// hostGoDownload runs `go mod download` in dir on the host with env; a test replaces it.
var hostGoDownload = func(ctx context.Context, dir string, env []string) error {
	_, err := hostOutput(ctx, dir, env, "go mod download", "go", "mod", "download")
	return err
}

// fillGoModules fills the person's module cache from the host before a Go repo's instance is made, so the sandbox only
// reads it. It runs once per group and again when go.mod or go.sum changed since the last fill.
func (m *Manager) fillGoModules(ctx context.Context, role string, spec sandbox.Spec) error {
	if spec.Modules == "" {
		return nil
	}

	sum, ok := goModSum(spec.Worktree)
	if !ok {
		return nil
	}

	m.fillMu.Lock()
	defer m.fillMu.Unlock()

	if m.filled == sum {
		return nil
	}

	env := append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	if err := hostGoDownload(ctx, spec.Worktree, env); err != nil {
		return block("setup", map[string]string{
			"role":   role,
			"output": err.Error(),
			"fix":    m.goModFix(),
		})
	}

	m.filled = sum
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
