package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// planStack returns a stack with just the configuration the sandbox plan reads.
func planStack(t *testing.T) *stack {
	t.Helper()

	cfg, err := config.New(
		t.TempDir(),
		config.Known{GuardRows: task.KnownRows(), Tools: task.KnownTools()},
		func(string) bool { return true },
	)
	if err != nil {
		t.Fatal(err)
	}

	return &stack{home: t.TempDir(), cfg: cfg}
}

func TestSandboxPlanAddsTheResolvedGolangciFolder(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	cellar, links := filepath.Join(root, "cellar", "bin"), filepath.Join(root, "links")
	for _, d := range []string{cellar, links} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cellar, "golangci-lint"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cellar, "golangci-lint"), filepath.Join(links, "golangci-lint")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", links+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := planStack(t).sandboxPlan("g1").Toolchains; !slices.Contains(got, cellar) || slices.Contains(got, links) {
		t.Fatalf("toolchains %v, want the resolved folder %s", got, cellar)
	}
}

func TestGolangciFolderIsEmptyWhenTheToolIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if got := golangciFolder(); got != "" {
		t.Fatalf("folder %q for a missing tool", got)
	}
}
