package task

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// ContextBriefer is the Briefer over the Context Manager: it hands GetContext a view of the base commit.
type ContextBriefer struct {
	Manager *contextmgr.Manager
	Tree    contextmgr.Tree
	Run     string
	Group   string
	Files   func() []string // the group's effective file list
	// the agent's chain with measured contexts; none when nil
	Candidates func(agent string) []contextmgr.Candidate
}

// Brief assembles one agent's brief and maps a brief that fits no link to ErrContext.
func (b *ContextBriefer) Brief(ctx context.Context, name string, slots map[string]string) (Brief, error) {
	vals := contextmgr.Values{}
	for k, v := range slots {
		vals[contextmgr.Slot(k)] = v
	}

	req := contextmgr.Request{Agent: name, Run: b.Run, Group: b.Group, Tree: b.Tree, Slots: vals}
	if b.Files != nil {
		req.Files = b.Files()
	}
	if b.Candidates != nil {
		req.Chain = b.Candidates(name)
	}

	got, err := b.Manager.GetContext(ctx, req)
	if err != nil {
		if errors.Is(err, contextmgr.ErrContext) {
			return Brief{}, fmt.Errorf("%w: %v", ErrContext, err)
		}
		return Brief{}, fmt.Errorf("assemble the %s brief: %w", name, err)
	}

	return Brief{Agent: name, Parts: got.Brief.Parts, Tokens: got.BriefTokens}, nil
}

// gitRunner is the part of the host git runner a tree view needs.
type gitRunner interface {
	Run(ctx context.Context, rest ...string) (string, error)
}

// BaseTree is a read-only view of the repository at the base commit, read through the host git rules.
type BaseTree struct {
	git  gitRunner
	base string
	once sync.Once
	list []string
	hash string
	err  error
}

var _ Briefer = (*ContextBriefer)(nil)

var _ contextmgr.Tree = (*BaseTree)(nil)

// NewBaseTree builds the view of the commit base.
func NewBaseTree(git gitRunner, base string) *BaseTree { return &BaseTree{git: git, base: base} }

// Hash returns the base commit's tree hash.
func (t *BaseTree) Hash() string {
	t.load()
	return t.hash
}

// Paths returns the tracked file paths in byte order.
func (t *BaseTree) Paths() []string {
	t.load()
	return slices.Clone(t.list)
}

// ReadFile returns the file's bytes at the base commit.
func (t *BaseTree) ReadFile(p string) ([]byte, error) {
	out, err := t.git.Run(context.Background(), "show", t.base+":"+path.Clean(p))
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Exists reports whether some tracked path matches the glob.
func (t *BaseTree) Exists(pattern string) bool {
	t.load()
	return slices.ContainsFunc(t.list, func(p string) bool { return agent.MatchGlob(pattern, p) })
}

// load lists the tracked files and the tree hash once.
func (t *BaseTree) load() {
	t.once.Do(func() {
		ctx := context.Background()
		h, err := t.git.Run(ctx, "rev-parse", t.base+"^{tree}")
		if err != nil {
			t.err = err
			return
		}

		out, err := t.git.Run(ctx, "ls-tree", "-r", "--name-only", t.base)
		if err != nil {
			t.err = err
			return
		}

		t.hash = strings.TrimSpace(h)

		for _, l := range strings.Split(out, "\n") {
			if l != "" {
				t.list = append(t.list, l)
			}
		}

		slices.Sort(t.list)
	})
}
