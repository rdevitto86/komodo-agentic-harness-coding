package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// The base commit's files, not the working tree's, land in the folder the Tester's sandbox runs the baseline in.
func TestCheckoutBaseWritesTheBaseCommitsFiles(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "empty"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "a.txt"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	base, err := gitOut("-C", repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("head"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := checkoutBase(context.Background(), repo, base, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "base" {
		t.Fatalf("a.txt = %q", b)
	}

	again, err := checkoutBase(context.Background(), repo, base, filepath.Dir(dir))
	if err != nil || again != dir {
		t.Fatalf("second checkout = %q %v", again, err)
	}
}

// The adapter keeps a baseline in the State Manager, where a later group on the same tree finds it.
func TestBaselineStoreRoundTripsThroughTheStateManager(t *testing.T) {
	st, err := state.New(t.TempDir(), "engine")
	if err != nil {
		t.Fatal(err)
	}

	err = baselineStore{st: st, group: "g1"}.Save(context.Background(), agent.Baseline{
		TreeHash: "t1",
		Passed:   map[string]bool{"a.T": true},
	})
	if err != nil {
		t.Fatal(err)
	}

	b, ok, err := baselineStore{st: st, group: "g2"}.Load(context.Background(), "t1")
	if err != nil || !ok || !b.Passed["a.T"] {
		t.Fatalf("load = %+v %v %v", b, ok, err)
	}
}
