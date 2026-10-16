package integration

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cleanup"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// One core.Git value serves both the Commit Handler and the Cleanup Handler.
var (
	_ cleanup.Git = (*core.Git)(nil)
	_ commit.Git  = (*core.Git)(nil)
)

// The git wrapper satisfies the interfaces of its two callers.
func TestGitWrapperSatisfiesCallers(t *testing.T) {
	var g any = core.NewGit()
	if _, ok := g.(cleanup.Git); !ok {
		t.Fatal("core.Git does not satisfy cleanup.Git")
	}
	if _, ok := g.(commit.Git); !ok {
		t.Fatal("core.Git does not satisfy commit.Git")
	}
}
