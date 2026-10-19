//go:build !windows

// Package integration holds tests that cross several subsystems.
package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/lease"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

const (
	run     = "0198f3a2-6c1e-7b44-9d0a-3f5e1c2b7a10"
	group   = "0198f3a2-7a10-7d3e-9b2f-6c1d8e4a5f30"
	repo    = "github.com/acme/shop"
	hash    = "023c648e364af6cf885bc15be12621607595a7ef64533179971d9e8d0603cb2c"
	version = "1.0.0-beta.6"
	payload = "repository map for the restart proof"
)

// TestStoresSurviveARestart writes a lease, a group record and a cache entry in a child process that then exits,
// and reads all three back in this process.
func TestStoresSurviveARestart(t *testing.T) {
	if root := os.Getenv("KOMODO_RESTART_WRITE"); root != "" {
		st, ls, c := stores(t, root)
		if _, err := ls.Grant(context.Background(), lease.GrantRequest{
			Repo: repo, Group: group, Run: run, Branch: "komodo/ep-1-tg-1.2", Worktree: "/wt/tg-1.2", ClonePath: "/src/shop",
		}); err != nil {
			t.Fatalf("grant: %v", err)
		}
		if _, err := st.Create(group, groupState()); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := c.Put(cache.NSMap, c.Key("map", repo), []byte(payload)); err != nil {
			t.Fatalf("put: %v", err)
		}
		return
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestStoresSurviveARestart$")
	cmd.Env = append(os.Environ(), "KOMODO_RESTART_WRITE="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writer process: %v\n%s", err, out)
	}
	st, ls, c := stores(t, root)
	l, ok, err := ls.Holder(context.Background(), repo, group)
	if err != nil || !ok || l.Run != run || l.Fence < 1 {
		t.Fatalf("lease after restart: %+v %v %v", l, ok, err)
	}
	r, err := st.Load(group)
	if err != nil || r.State.Run != run || r.State.Stage != state.StageBuild {
		t.Fatalf("state after restart: %+v %v", r, err)
	}
	got, hit := c.Get(cache.NSMap, c.Key("map", repo))
	if !hit || string(got) != payload {
		t.Fatalf("cache after restart: %q %v", got, hit)
	}
}

// stores opens the three phase-3 stores under one root, as a fresh process would.
func stores(t *testing.T, root string) (*state.Manager, *lease.Manager, *cache.Cache) {
	t.Helper()
	st, err := state.New(filepath.Join(root, "repo"), "engine")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	ls, err := lease.New(filepath.Join(root, "home"), 30, nil, nil)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	c, err := cache.New(filepath.Join(root, "cache"), version, cache.Limits{})
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	return st, ls, c
}

// groupState returns a valid group record for the proof.
func groupState() state.GroupState {
	return state.GroupState{
		Run: run, Group: group, Stage: state.StageBuild, Epoch: 1, Fence: 1, ConfigHash: hash,
		Counters:    state.Counters{Blocks: map[string]int{}},
		Tasks:       []state.TaskState{},
		TestReview:  state.TestReview{Findings: []state.Finding{}, NotMet: []string{}, Failed: []string{}},
		ActionsOpen: []state.OpenAction{}, Resume: []state.ResumeRef{}, Owed: []string{},
		Requests: []string{}, ScopeAdded: []string{},
		LastEventAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
	}
}
