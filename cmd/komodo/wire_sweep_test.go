//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// idOther is the group of the plan that no group state mentions.
const idOther = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f77"

// sweepRig opens a kit for testRepo, with a merged plan whose groups are done, an 8-day-old plan nothing uses, and a
// failed draft; it returns the hub and the three folders.
func sweepRig(t *testing.T) (h *hub, merged, old, failed string) {
	t.Helper()

	k, s := commitKit(t)

	k, err := s.kitFor(t.TempDir(), k.common, testRepo)
	if err != nil {
		t.Fatal(err)
	}

	k.commit.Forge = &mergedForge{merged: true}

	writePlan(t, s.home, "plan-done")
	for _, id := range []string{idOne, idTwo} {
		if _, err := k.st.Create(id, groupRecord(id, state.StageDone, "complete")); err != nil {
			t.Fatal(err)
		}
	}

	bp := intake.Blueprint{
		Plan: "plan-old", Repo: intake.RepoFolder(testRepo), Goal: "Idle", Release: "V1",
		Groups: []intake.Group{{
			ID: idOther, Name: "tg-1", Goal: "Idle", Files: []string{"c.go"}, Priority: intake.PriorityNormal,
			Tasks: []intake.Task{{Name: "tsk-d"}},
		}},
	}
	if err := intake.WriteBlueprint(s.home, bp); err != nil {
		t.Fatal(err)
	}

	folder := intake.RepoFolder(testRepo)
	merged = intake.PlanDir(s.home, folder, "plan-done")
	old = intake.PlanDir(s.home, folder, "plan-old")
	failed = filepath.Join(s.home, "plans", folder, ".plan-failed-ab12")

	if err := os.MkdirAll(failed, 0o755); err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(-8 * 24 * time.Hour)
	for _, f := range []string{old, filepath.Join(old, "blueprint.json")} {
		if err := os.Chtimes(f, when, when); err != nil {
			t.Fatal(err)
		}
	}

	return &hub{s}, merged, old, failed
}

// The engine-start sweep removes a finished, merged plan folder and a failed draft, and keeps an 8-day-old unused plan.
func TestHubSweep(t *testing.T) {
	t.Run("a start sweep removes the merged plan and the failed draft and keeps the idle plan", func(t *testing.T) {
		h, merged, old, failed := sweepRig(t)

		if err := h.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(merged); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the merged plan folder survives: %v", err)
		}
		if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the failed draft survives: %v", err)
		}
		if _, err := os.Stat(old); err != nil {
			t.Fatalf("the 8-day-old plan went: %v", err)
		}
	})

	t.Run("a plan whose pull request is open stays", func(t *testing.T) {
		h, merged, _, _ := sweepRig(t)

		for _, k := range h.s.kits {
			k.commit.Forge = &mergedForge{}
		}
		if err := h.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(merged); err != nil {
			t.Fatalf("an unmerged plan went: %v", err)
		}
	})
}
