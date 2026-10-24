//go:build !windows

package main

import (
	"path/filepath"
	"testing"
)

// A repository id with a slash gets the full-name worktree folder and the short-name one as the folder to migrate.
func TestWorktreeRoots(t *testing.T) {
	t.Run("the kit names the full folder and the old one", func(t *testing.T) {
		k, s := commitKit(t)

		k2, err := s.kitFor(t.TempDir(), k.common, "github.com/acme/shop")
		if err != nil {
			t.Fatal(err)
		}

		r := k2.cleanup.Roots
		if want := filepath.Join(s.home, "wt", "github.com-acme-shop"); r.Worktrees != want {
			t.Fatalf("worktrees %q, want %q", r.Worktrees, want)
		}
		if want := filepath.Join(s.home, "wt", "shop"); r.Legacy != want {
			t.Fatalf("legacy %q, want %q", r.Legacy, want)
		}
	})

	t.Run("an id with no slash has nothing to migrate", func(t *testing.T) {
		if got := legacyWorktrees("/h", "shop"); got != "" {
			t.Fatalf("legacy %q", got)
		}
	})
}
