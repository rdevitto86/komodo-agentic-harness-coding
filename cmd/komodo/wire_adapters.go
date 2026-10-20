package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"os/exec"
	"slices"
	"strconv"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cache"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cleanup"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// stateStore adds a context to the State Manager's calls.
type stateStore struct{ m *state.Manager }

// Load returns a group's record.
func (s stateStore) Load(_ context.Context, g string) (state.Record, error) { return s.m.Load(g) }

// Create writes a group's first record.
func (s stateStore) Create(_ context.Context, g string, st engine.State) (state.Record, error) {
	return s.m.Create(g, st)
}

// Save writes a group's record at the revision after rev.
func (s stateStore) Save(_ context.Context, g string, st engine.State, rev int) (state.Record, error) {
	return s.m.Save(g, st, rev)
}

// ctxAdapter gives the Runtime Manager the Context Manager's cache hook and a per-agent context.
type ctxAdapter struct{}

// UseCache hands the repository's cache handle to the Context Manager.
func (c ctxAdapter) UseCache(string, cache.Handle) {}

// GetContext returns the stage context of an agent; the Task Manager assembles its own briefs.
func (c ctxAdapter) GetContext(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

// planSource reads the groups of a run from the task lists under the repository root.
type planSource struct{}

// Specs returns the group specs for the labels, in task-list order.
func (p planSource) Specs(_ context.Context, root string, labels []string) ([]engine.GroupSpec, error) {
	epics, _, err := intake.Load(root)
	if err != nil {
		return nil, fmt.Errorf("read the task lists: %w", err)
	}

	ids := map[string]string{}
	for _, e := range epics {
		for _, g := range e.Groups {
			ids[g.Name] = g.ID
		}
	}

	var out []engine.GroupSpec

	order := 0

	for _, e := range epics {
		for _, g := range e.Groups {
			order++
			if !slices.Contains(labels, g.Name) {
				continue
			}

			sp := engine.GroupSpec{
				ID:       g.ID,
				Label:    g.Name,
				Epic:     "ep-" + strconv.Itoa(e.Number),
				Files:    g.Files,
				Priority: string(g.Priority),
				Order:    order,
			}
			for _, d := range g.Dependencies {
				sp.Depends = append(sp.Depends, ids[d])
			}

			for _, t := range g.Tasks {
				sp.Tasks = append(sp.Tasks, t.Name)
			}

			out = append(out, sp)
		}
	}

	for _, l := range labels {
		if !slices.ContainsFunc(out, func(s engine.GroupSpec) bool { return s.Label == l }) {
			return nil, fmt.Errorf("unknown group %s", l)
		}
	}

	return out, nil
}

// epicGit adopts a clone before the branch calls the Runtime Manager makes in it.
type epicGit struct{ g *core.Git }

// BranchExists reports whether the epic branch exists in the clone.
func (e epicGit) BranchExists(ctx context.Context, clone, branch string) (bool, error) {
	e.g.Adopt(clone)
	return e.g.BranchExists(ctx, clone, branch)
}

// CreateBranch cuts the epic branch from base in the clone.
func (e epicGit) CreateBranch(ctx context.Context, clone, branch, base string) error {
	e.g.Adopt(clone)
	return e.g.CreateBranch(ctx, clone, branch, base)
}

// DefaultBranch names the branch the remote's HEAD points to.
func (e epicGit) DefaultBranch(ctx context.Context, dir, remote string) (string, error) {
	e.g.Adopt(dir)
	return e.g.DefaultBranch(ctx, dir, remote)
}

// UpstreamRemote names the remote a local branch tracks; false when it tracks none.
func (e epicGit) UpstreamRemote(ctx context.Context, dir, branch string) (string, bool, error) {
	e.g.Adopt(dir)
	return e.g.UpstreamRemote(ctx, dir, branch)
}

// FetchBranch brings one remote branch into its remote-tracking reference.
func (e epicGit) FetchBranch(ctx context.Context, dir, remote, branch string) error {
	e.g.Adopt(dir)
	return e.g.FetchBranch(ctx, dir, remote, branch)
}

// janitor runs the machine-wide clean through the Cleanup Handler.
type janitor struct{ h *cleanup.Handler }

// Clean removes, or with dryRun lists, what belongs to no open run.
func (j janitor) Clean(ctx context.Context, dryRun bool, _ map[string]bool) ([]engine.CleanItem, error) {
	var (
		rep cleanup.Report
		err error
	)
	if dryRun {
		rep, err = j.h.Preview(ctx)
	} else {
		rep, err = j.h.Sweep(ctx)
	}
	if err != nil {
		return nil, err
	}

	return cleanItems(rep), nil
}

// publishedAdapter drops the report the Cleanup Handler returns, which the Commit Handler does not read.
type publishedAdapter struct {
	h       *cleanup.Handler
	landing func(epic string) string
}

// Complete cleans up one published group.
func (p publishedAdapter) Complete(ctx context.Context, pub commit.Published) error {
	_, err := p.h.Complete(ctx, cleanup.Published{
		Run:      string(pub.Run),
		Group:    string(pub.Group),
		Epic:     pub.Epic,
		Branch:   pub.Branch,
		Tip:      pub.Tip,
		Remote:   pub.Remote,
		Contains: pub.Contains,
		Worktree: pub.Worktree,
		Landing:  p.landing(pub.Epic),
		EpicDone: pub.EpicDone,
		PRURL:    pub.PRURL,
	})

	return err
}

// noContainers is the container list when Docker is not in use.
type noContainers struct{}

// Remove removes nothing.
func (noContainers) Remove(context.Context, string) error { return nil }

// List lists nothing.
func (noContainers) List(context.Context) ([]string, error) { return nil, nil }

// unfinished lists the groups whose records are not done, for the cleanup sweep; the records pin no binary version, so each
// names the running one.
type unfinished struct{ st *state.Manager }

// Unfinished returns the groups a `run` could still continue.
func (u unfinished) Unfinished(context.Context) ([]cleanup.GroupRef, error) {
	recs, err := u.st.List()
	if err != nil {
		return nil, err
	}

	var out []cleanup.GroupRef
	for _, r := range recs {
		if r.State.Stage != state.StageDone {
			out = append(out, cleanup.GroupRef{Group: r.Header.Group, Version: version})
		}
	}

	return out, nil
}

// groupLocks answers whether a group is in a run from the repository's group records.
type groupLocks struct{ st *state.Manager }

// Compile-time proof that the host git wrapper serves the Commit and Cleanup handlers.
var (
	_ cleanup.Git = (*core.Git)(nil)
	_ commit.Git  = (*core.Git)(nil)
)

// InRun reports the run that holds a group whose record is not done.
func (g groupLocks) InRun(group string) (string, bool, error) {
	rec, err := g.st.Peek(group)
	if errors.Is(err, state.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return rec.State.Run, rec.State.Stage != state.StageDone, nil
}

// containers returns the Docker container list when docker is installed, else an empty one.
func (s *stack) containers() cleanup.Containers {
	if _, err := exec.LookPath("docker"); err != nil {
		return noContainers{}
	}
	return sandbox.NewDocker(sandbox.DockerOptions{})
}

// jsonOf encodes a value; nil encodes to nothing.
func jsonOf(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// jsonRaw encodes a value that cannot fail to encode.
func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// cleanItems lists what a cleanup report removed.
func cleanItems(r cleanup.Report) []engine.CleanItem {
	var out []engine.CleanItem
	for _, it := range r.Removed {
		out = append(out, engine.CleanItem{Kind: it.Kind, Name: it.Name})
	}
	return out
}
