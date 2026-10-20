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

// commitKit opens a repository kit and returns it with the stack; the changes are applied before the kit opens.
func commitKit(t *testing.T, changes ...config.Change) (*repoKit, *stack) {
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

	return k, s
}

// commitKitSettings returns the settings the kit's Commit Handler reads after the changes were applied before it opened.
func commitKitSettings(t *testing.T, changes ...config.Change) (commit.Settings, string) {
	t.Helper()

	k, _ := commitKit(t, changes...)

	return k.commit.Live(), k.landings
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

// A setting changed after the engine and the kit started reaches the next run's epic cut and the Commit Handler alike.
func TestCommitSettingsAreReadLiveByTheEngineAndTheHandler(t *testing.T) {
	k, s := commitKit(t)

	if _, err := s.cfg.SetConfigItem(
		config.Change{Key: "commit.base_branch", New: "late-base"},
		config.Change{Key: "commit.remote", New: "late-remote"},
	); err != nil {
		t.Fatal(err)
	}

	if got := k.commit.Live(); got.BaseBranch != "late-base" || got.Remote != "late-remote" {
		t.Fatalf("the Commit Handler reads %+v after the change", got)
	}
	if got, want := s.baseBranch(), "late-base"; got != want {
		t.Fatalf("the engine reads base %q, want %q", got, want)
	}
	if got, want := s.remote(), "late-remote"; got != want {
		t.Fatalf("the engine reads remote %q, want %q", got, want)
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
