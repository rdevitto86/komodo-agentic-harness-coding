package engine

import (
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// A group's worktree sits under the repository's full folder name, the same name the plan folders use.
func TestWorktreeOfUsesTheFullRepositoryName(t *testing.T) {
	t.Parallel()

	g := &group{
		m:    &Manager{d: Deps{Home: "/h"}},
		r:    &run{start: bus.RunStart{Repo: "github.com/acme/shop"}},
		spec: GroupSpec{ID: "g1"},
	}

	if got, want := g.worktreeOf(), filepath.Join("/h", "wt", "github.com-acme-shop", "g1"); got != want {
		t.Fatalf("worktree %q, want %q", got, want)
	}
}
