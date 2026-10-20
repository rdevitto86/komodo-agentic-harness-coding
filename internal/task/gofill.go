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

	err := fillWithCopy(ctx, spec.Worktree)
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

// fillWithCopy runs the host download in dir against a private copy of go.mod and go.sum, so the worktree's own files are
// never written; the copy is removed afterwards.
func fillWithCopy(ctx context.Context, dir string) error {
	tmp, err := os.MkdirTemp("", "komodo-gofill-")
	if err != nil {
		return fmt.Errorf("make the go.mod copy folder: %w", err)
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(tmp, name), data, 0o600)
		}
		if err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}

	env := append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod -modfile="+filepath.Join(tmp, "go.mod"))

	return hostGoDownload(ctx, dir, env)
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
