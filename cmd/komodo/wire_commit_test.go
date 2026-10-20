//go:build !windows

package main

import (
	"context"
	"io"
	"os/exec"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
)

// commitKitSettings opens a repository kit after applying the changes and returns its Commit Handler's settings.
func commitKitSettings(t *testing.T, changes ...config.Change) (commit.Settings, string) {
	t.Helper()

	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = root

	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}

	t.Setenv("HOME", t.TempDir())

	s, err := newStack(t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx

	t.Cleanup(func() { cancel(); s.close() })

	if len(changes) > 0 {
		if _, err := s.cfg.SetConfigItem(changes...); err != nil {
			t.Fatal(err)
		}
	}

	k, err := s.kitFor(root, "", "repo")
	if err != nil {
		t.Fatal(err)
	}

	return k.commit.Config, k.landings
}

// The Commit Handler receives the base branch, remote and pull request label the person set.
func TestCommitHandlerReceivesTheCommitSettings(t *testing.T) {
	got, landings := commitKitSettings(
		t,
		config.Change{Key: "commit.base_branch", New: "komodo-first-run"},
		config.Change{Key: "commit.remote", New: "trial"},
		config.Change{Key: "commit.pr_label", New: "first-run"},
	)

	want := commit.Settings{
		BaseBranch:  "komodo-first-run",
		Remote:      "trial",
		PRLabel:     "first-run",
		LandingRoot: landings,
	}
	if got != want {
		t.Fatalf("settings %+v, want %+v", got, want)
	}
}

// With nothing set, the handler takes the remote's default branch and the label komodo.
func TestCommitHandlerSettingsDefaults(t *testing.T) {
	got, landings := commitKitSettings(t)

	want := commit.Settings{PRLabel: "komodo", LandingRoot: landings}
	if got != want {
		t.Fatalf("settings %+v, want %+v", got, want)
	}
}
