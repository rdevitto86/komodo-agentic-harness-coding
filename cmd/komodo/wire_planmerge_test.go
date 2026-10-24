//go:build !windows

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// mergedForge is a Forge that answers the merged-pull-request question and records the branch asked about.
type mergedForge struct {
	commit.Forge
	merged bool
	err    error
	asked  string
}

// MergedPR returns the configured answer.
func (f *mergedForge) MergedPR(_ context.Context, head string) (bool, error) {
	f.asked = head
	return f.merged, f.err
}

// The Cleanup Handler asks the forge about the plan's epic branch.
func TestPlanMerger(t *testing.T) {
	t.Run("a merged pull request of the plan's epic branch reports merged", func(t *testing.T) {
		k, _ := commitKit(t)
		f := &mergedForge{merged: true}
		k.commit.Forge = f

		got, err := planMerger{k}.PlanMerged(context.Background(), "plan-aaaaaa")
		if err != nil || !got || f.asked != "komodo/plan-aaaaaa" {
			t.Fatalf("merged %v, err %v, asked %q", got, err, f.asked)
		}
	})

	t.Run("a forge error is returned", func(t *testing.T) {
		k, _ := commitKit(t)
		k.commit.Forge = &mergedForge{err: errors.New("forge down")}

		if _, err := (planMerger{k}).PlanMerged(context.Background(), "plan-aaaaaa"); err == nil {
			t.Fatal("a forge error was dropped")
		}
	})

	t.Run("the kit's cleanup handler carries the merge check", func(t *testing.T) {
		k, _ := commitKit(t)

		if k.cleanup.Merged == nil {
			t.Fatal("the Cleanup Handler has no merge check")
		}
	})
}
