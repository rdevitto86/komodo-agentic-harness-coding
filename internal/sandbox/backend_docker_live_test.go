package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveImage is a local image that holds git; the live Docker tests skip when it is not pulled.
const liveImage = "golang:1.27"

// liveGitWorktree makes a clone with one commit and a linked worktree, and returns the worktree and its own git folder.
func liveGitWorktree(t *testing.T) (worktree, gitDir string) {
	t.Helper()

	root := t.TempDir()
	clone, worktree := filepath.Join(root, "clone"), filepath.Join(root, "wt")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "a.txt"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "one"},
		{"worktree", "add", "-q", "-b", "side", worktree},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	return worktree, filepath.Join(clone, ".git", "worktrees", "wt")
}

// A container reads the worktree's git history through the pointer file, and nothing in it writes git.
func TestDockerLiveReadsGitAndCannotWriteIt(t *testing.T) {
	d := NewDocker(DockerOptions{Image: liveImage})
	if err := d.Check(); err != nil {
		t.Skip(err)
	}
	if err := exec.Command("docker", "image", "inspect", liveImage).Run(); err != nil {
		t.Skipf("image %s is not pulled", liveImage)
	}

	worktree, gitDir := liveGitWorktree(t)
	s := Spec{Group: "live", Worktree: worktree, GitDir: gitDir, Writable: []string{t.TempDir()}, Role: RoleQA}

	m := NewManager(map[string]Backend{RoleQA: d}, nil)
	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DestroySandboxInstance(context.Background(), inst.ID) })

	run := func(script string) (string, error) {
		cmd, err := m.Command(context.Background(), inst.ID, Command{
			Argv: []string{"/bin/sh", "-c", script}, Timeout: 30 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, script := range []string{"git cat-file -e HEAD:a.txt", "git log -1 --format=%s", "git cat-file -e main:a.txt"} {
		if out, err := run(script); err != nil {
			t.Fatalf("%s failed: %v\n%s", script, err, out)
		}
	}
	if out, _ := run("git log -1 --format=%s"); strings.TrimSpace(out) != "one" {
		t.Fatalf("git log read %q, want one", out)
	}

	for _, script := range []string{
		"echo b > b.txt && git add b.txt && git -c user.name=t -c user.email=t@t commit -m two",
		"echo x > .git",
		"git update-ref refs/heads/evil HEAD",
		"echo x > " + filepath.Join(filepath.Dir(filepath.Dir(gitDir)), "evil"),
	} {
		if out, err := run(script); err == nil {
			t.Fatalf("%s succeeded, but git is read-only in a container\n%s", script, out)
		}
	}

	if out, err := exec.Command("git", "-C", worktree, "log", "-1", "--format=%s").CombinedOutput(); err != nil ||
		strings.TrimSpace(string(out)) != "one" {
		t.Fatalf("the host history changed: %v %s", err, out)
	}
}

// A worktree's git folder mounts as the shared folder its commondir file names; a plain git folder mounts as itself.
func TestGitSharedFollowsCommondir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	own := filepath.Join(root, "clone", ".git", "worktrees", "wt")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitShared(own); got != own {
		t.Fatalf("without commondir got %q, want %q", got, own)
	}
	if err := os.WriteFile(filepath.Join(own, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := gitShared(own), filepath.Join(root, "clone", ".git"); got != want {
		t.Fatalf("with commondir got %q, want %q", got, want)
	}
	if got := gitShared(filepath.Join(root, "missing")); got != "" {
		t.Fatalf("a missing folder got %q, want empty", got)
	}
	if got := gitShared(""); got != "" {
		t.Fatalf("no git folder got %q, want empty", got)
	}
}
