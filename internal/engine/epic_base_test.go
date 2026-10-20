//go:build !windows

package engine

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// gitIn runs git in dir with a fixed identity and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

// baseFixture builds a remote whose default branch is komodo-first-run and a clone that has no such local branch.
// It returns the clone, the remote's tip and the clone's own checked-out commit.
func baseFixture(t *testing.T) (clone, tip, mine string) {
	t.Helper()

	root := t.TempDir()
	remote, seed, clone := filepath.Join(root, "remote.git"), filepath.Join(root, "seed"), filepath.Join(root, "clone")

	gitIn(t, root, "init", "-q", "--bare", "-b", "komodo-first-run", remote)
	gitIn(t, root, "init", "-q", "-b", "komodo-first-run", seed)
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, seed, "remote", "add", "trial", remote)
	gitIn(t, seed, "push", "-q", "trial", "komodo-first-run")

	gitIn(t, root, "init", "-q", "-b", "main", clone)
	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "mine")
	gitIn(t, clone, "remote", "add", "trial", remote)
	gitIn(t, clone, "fetch", "-q", "trial")

	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "newer")
	gitIn(t, seed, "push", "-q", "trial", "komodo-first-run")

	return clone, gitIn(t, seed, "rev-parse", "HEAD"), gitIn(t, clone, "rev-parse", "HEAD")
}

// managerOver returns a Manager that reaches git through the real wrapper, adopted for the clone.
func managerOver(clone, base, remote string) *Manager {
	g := core.NewGit()
	g.Adopt(clone)

	return &Manager{d: Deps{Git: g, BaseBranch: base, Remote: remote}}
}

// With only a stale remote-tracking base, the epic branch lands on the remote's current tip and the clone is untouched.
func TestEpicBranchIsCutAtTheRemoteTipWithNoLocalBase(t *testing.T) {
	clone, tip, mine := baseFixture(t)
	m := managerOver(clone, "komodo-first-run", "trial")

	if err := m.cutEpicBranch(context.Background(), clone, "komodo/ep-1"); err != nil {
		t.Fatal(err)
	}

	if got := gitIn(t, clone, "rev-parse", "komodo/ep-1"); got != tip {
		t.Fatalf("epic branch at %s, want the remote tip %s", got, tip)
	}
	if got := gitIn(t, clone, "branch", "--list", "komodo-first-run"); got != "" {
		t.Fatalf("a local base branch was created: %q", got)
	}
	if got := gitIn(t, clone, "rev-parse", "HEAD"); got != mine || gitIn(t, clone, "branch", "--show-current") != "main" {
		t.Fatalf("the clone's checkout moved to %s", got)
	}
}

// With an empty base the epic branch is cut from the remote's default branch.
func TestEpicBranchIsCutFromTheRemoteDefaultWhenTheBaseIsEmpty(t *testing.T) {
	clone, tip, _ := baseFixture(t)
	m := managerOver(clone, "", "trial")

	if err := m.cutEpicBranch(context.Background(), clone, "komodo/ep-1"); err != nil {
		t.Fatal(err)
	}

	if got := gitIn(t, clone, "rev-parse", "komodo/ep-1"); got != tip {
		t.Fatalf("epic branch at %s, want the remote default's tip %s", got, tip)
	}
}
