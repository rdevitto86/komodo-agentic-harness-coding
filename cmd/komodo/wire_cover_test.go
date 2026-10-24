//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
)

// plannedKit opens a second kit for testRepo over the git folder of the first, so plan folders of testRepo are its own.
func plannedKit(t *testing.T) (*repoKit, *stack) {
	t.Helper()

	k, s := commitKit(t)

	k2, err := s.kitFor(t.TempDir(), k.common, testRepo)
	if err != nil {
		t.Fatal(err)
	}

	return k2, s
}

// writeLease writes a lease record where the lease registry keeps them.
func writeLease(t *testing.T, home, name string, l lease.Lease) {
	t.Helper()

	dir := filepath.Join(home, "leases", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedAdapterComplete(t *testing.T) {
	t.Run("a group inside the worktree folder is cleaned up and closed", func(t *testing.T) {
		k, _ := plannedKit(t)
		writePlan(t, k.s.home, "plan-1")

		wt := filepath.Join(k.cleanup.Roots.Worktrees, idOne)
		err := k.published(k.cleanup).Complete(context.Background(), commit.Published{
			Run: bus.ID(testRun), Group: bus.ID(idOne), Epic: "plan-1", Tip: "abc", Landed: "def", Into: "komodo/plan-1",
			Worktree: wt,
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a worktree outside the folder is refused", func(t *testing.T) {
		k, _ := plannedKit(t)

		err := k.published(k.cleanup).Complete(context.Background(), commit.Published{
			Group: bus.ID(idOne), Epic: "plan-1", Landed: "def", Worktree: "/elsewhere",
		})
		if err == nil {
			t.Fatal("a worktree outside the folder was accepted")
		}
	})
}

func TestHubEpicMerged(t *testing.T) {
	t.Run("the repository's Commit Handler answers", func(t *testing.T) {
		k, s := plannedKit(t)
		f := &mergedForge{merged: true}
		k.commit.Forge = f

		got, err := (&hub{s}).EpicMerged(context.Background(), commit.EpicPush{
			Root: k.root, CommonDir: k.common, Repo: k.id, Epic: "plan-1",
		})
		if err != nil || !got || f.asked != "komodo/plan-1" {
			t.Fatalf("merged %v, err %v, asked %q", got, err, f.asked)
		}
	})

	t.Run("a request with no root is refused", func(t *testing.T) {
		_, s := plannedKit(t)

		if _, err := (&hub{s}).EpicMerged(context.Background(), commit.EpicPush{Epic: "plan-1"}); err == nil {
			t.Fatal("a request with no root was accepted")
		}
	})
}

func TestRepoIDOfAndBlueprint(t *testing.T) {
	t.Run("repoIDOf returns the id of the kit that serves the root", func(t *testing.T) {
		k, s := plannedKit(t)

		if got, err := s.repoIDOf(k.root); err != nil || got != testRepo {
			t.Fatalf("id %q, err %v", got, err)
		}
		if _, err := s.repoIDOf(""); err == nil {
			t.Fatal("an empty root was accepted")
		}
	})

	t.Run("blueprint reads a plan of this repository", func(t *testing.T) {
		k, _ := plannedKit(t)
		writePlan(t, k.s.home, "plan-1")

		bp, err := k.blueprint("plan-1")
		if err != nil || len(bp.Groups) != 2 || bp.Repo != intake.RepoFolder(testRepo) {
			t.Fatalf("blueprint %+v, err %v", bp, err)
		}
		if _, err := k.blueprint("plan-none"); err == nil {
			t.Fatal("a missing plan was read")
		}
	})
}

func TestParkedGroups(t *testing.T) {
	t.Run("a lease names its clone, git folder and group label", func(t *testing.T) {
		k, s := commitKit(t)
		writePlan(t, s.home, "plan-1")
		writeLease(t, s.home, "a", lease.Lease{Repo: testRepo, Group: idOne, ClonePath: k.root})
		writeLease(t, s.home, "b", lease.Lease{Repo: testRepo, Group: idTwo, ClonePath: k.root})

		got := s.parkedGroups(context.Background())
		if len(got) != 2 {
			t.Fatalf("parked %+v", got)
		}
		for _, p := range got {
			want := map[string]string{idOne: "tg-1", idTwo: "tg-2"}[p.Group]
			if p.Label != want || p.Root != k.root || p.CommonDir == "" || p.Repo != testRepo {
				t.Fatalf("parked %+v, want label %q", p, want)
			}
		}
	})

	t.Run("a broken record, a record with no group and a clone that is not a repository are skipped", func(t *testing.T) {
		k, s := commitKit(t)
		writeLease(t, s.home, "c", lease.Lease{Repo: testRepo, Group: idOne, ClonePath: t.TempDir()})
		writeLease(t, s.home, "d", lease.Lease{Repo: testRepo, ClonePath: k.root})

		if err := os.WriteFile(filepath.Join(s.home, "leases", "r", "e.json"), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := s.parkedGroups(context.Background()); len(got) != 0 {
			t.Fatalf("parked %+v", got)
		}
	})
}

func TestHubHosted(t *testing.T) {
	t.Run("a lease's repository joins the kits the engine serves", func(t *testing.T) {
		k, s := plannedKit(t)
		other := t.TempDir()

		writeLease(t, s.home, "a", lease.Lease{Repo: "github.com/acme/other", Group: idOne, ClonePath: other})
		if got := (&hub{s}).hosted(k.root); len(got) < 1 {
			t.Fatalf("kits %d", len(got))
		}
	})

	t.Run("Sweep reports a repository it cannot sweep", func(t *testing.T) {
		k, s := plannedKit(t)
		k.cleanup.Roots.Worktrees = ""

		if err := (&hub{s}).Sweep(context.Background()); err == nil {
			t.Fatal("a failed sweep was dropped")
		}
	})
}
