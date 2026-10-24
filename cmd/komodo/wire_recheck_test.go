package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoWith makes a repository with one commit holding the file and returns its folder.
func repoWith(t *testing.T, file, body string) string {
	t.Helper()
	dir := t.TempDir()
	setup := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	}
	for _, args := range setup {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// Before the landing the checks pass without running; after it a landing worktree whose tree is not the group's is an error,
// never a pass.
func TestGroupCheckerRefusesATreeMismatchAfterTheLanding(t *testing.T) {
	group := repoWith(t, "a.txt", "fixed")
	landing := repoWith(t, "a.txt", "broken")
	c := groupChecker{ref: &groupRef{}, worktree: group}

	res, err := c.Recheck(t.Context(), "ep-1", landing, false)
	if err != nil || !res.Pass {
		t.Fatalf("before the landing: %+v, %v", res, err)
	}

	res, err = c.Recheck(t.Context(), "ep-1", landing, true)
	if err == nil || res.Pass || !strings.Contains(err.Error(), "differs from the epic tip") {
		t.Fatalf("after the landing: %+v, %v", res, err)
	}
}
