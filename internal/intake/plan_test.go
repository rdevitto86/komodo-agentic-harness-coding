package intake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

var registerOnce sync.Once

// fakeTree is a read-only tree of the base commit held in a map.
type fakeTree map[string]string

func (f fakeTree) Hash() string { return "tree" }

func (f fakeTree) ReadFile(p string) ([]byte, error) {
	if s, ok := f[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

func (f fakeTree) Exists(pattern string) bool {
	return slices.ContainsFunc(f.Paths(), func(p string) bool { return agent.MatchPath(pattern, p) })
}

func (f fakeTree) Paths() []string {
	var p []string
	for k := range f {
		p = append(p, k)
	}
	sort.Strings(p)
	return p
}

// fakeContext returns a canned brief and records the request.
type fakeContext struct {
	err error
	req contextmgr.Request
}

func (f *fakeContext) GetContext(_ context.Context, req contextmgr.Request) (contextmgr.Context, error) {
	f.req = req
	if f.err != nil {
		return contextmgr.Context{}, f.err
	}
	return contextmgr.Context{
		Brief: contextmgr.Brief{
			Agent: "planner",
			Parts: [5]string{"", "ROLE", "", "", "BRIEF " + req.Slots[contextmgr.Goal]},
		},
	}, nil
}

// fakeLocks answers InRun from a map of group id to run.
type fakeLocks struct {
	runs     map[string]string
	finished map[string][]string
	err      error
}

// Finished answers the finished task names of a group id from the map.
func (f fakeLocks) Finished(id string) ([]string, error) { return f.finished[id], f.err }

func (f fakeLocks) InRun(id string) (string, bool, error) {
	r, ok := f.runs[id]
	return r, ok, f.err
}

// scripted is a planner session that answers each Send with the next scripted step.
type scripted struct {
	mu      sync.Mutex
	steps   []any // PlanDraft, error or func(ctx) (PlanDraft, error)
	texts   []string
	started int
	closed  int
}

func (s *scripted) Send(ctx context.Context, text string) (PlanDraft, error) {
	s.mu.Lock()
	s.texts = append(s.texts, text)
	if len(s.steps) == 0 {
		s.mu.Unlock()
		return PlanDraft{}, errors.New("script ran out")
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	s.mu.Unlock()
	switch v := step.(type) {
	case PlanDraft:
		return v, nil
	case error:
		return PlanDraft{}, v
	case func(context.Context) (PlanDraft, error):
		return v(ctx)
	}
	return PlanDraft{}, errors.New("bad step")
}

func (s *scripted) Close() error { s.mu.Lock(); s.closed++; s.mu.Unlock(); return nil }

func (s *scripted) start(context.Context) (PlannerSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started++
	return s, nil
}

// rig is a handler wired to fakes over a temporary home and a temporary repository.
type rig struct {
	t    *testing.T
	home string
	root string
	h    *Handler
	bus  bus.Bus
	ctx  *fakeContext
	sess *scripted
	tree fakeTree
	lock fakeLocks
}

// newRig builds a handler whose planner session plays the given steps.
func newRig(t *testing.T, steps ...any) *rig {
	t.Helper()
	registerOnce.Do(RegisterEvents)
	r := &rig{
		t: t, home: t.TempDir(), root: t.TempDir(), bus: bus.New(bus.Options{}), ctx: &fakeContext{},
		sess: &scripted{steps: steps},
		tree: fakeTree{"README.md": "readme", "docs/hld.md": "the design"},
		lock: fakeLocks{runs: map[string]string{}, finished: map[string][]string{}},
	}
	t.Cleanup(func() { _ = r.bus.Close() })
	r.h = New(Deps{
		Bus:       r.bus,
		Context:   r.ctx,
		Start:     r.sess.start,
		Locks:     r.lock,
		Protected: func() []string { return []string{".github/**"} },
		Limits:    PlannerLimits{WallMin: 1},
		Home:      r.home,
		Root:      r.root,
		Tree:      func(context.Context) (contextmgr.Tree, error) { return r.tree, nil },
		Repo:      "repo-1", CommonDir: filepath.Join(r.root, ".git"),
	})
	return r
}

// plans returns the folder that holds the plan folders of the rig's repository.
func (r *rig) plans() string { return filepath.Join(r.home, "plans", "repo-1") }

// entries lists the names directly under the plans folder of the repository.
func (r *rig) entries() []string {
	es, _ := os.ReadDir(r.plans())
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// repoFiles counts the files under the repository root.
func (r *rig) repoFiles() int {
	n := 0
	_ = filepath.WalkDir(r.root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestPlan(t *testing.T) {
	t.Parallel()

	t.Run("a passing draft writes plan.md and blueprint.json under the home", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "Add revocation", Docs: []string{"docs/hld.md"}})
		if err != nil {
			t.Fatal(err)
		}
		if !planIDShape.MatchString(res.Plan) || !slices.Equal(res.Groups, []string{"tg-1", "tg-2"}) {
			t.Fatalf("result: %+v", res)
		}
		dir := filepath.Join(r.plans(), res.Plan)
		if res.Path != filepath.Join(dir, "plan.md") {
			t.Fatalf("path %s", res.Path)
		}
		if got := r.entries(); !slices.Equal(got, []string{res.Plan}) {
			t.Fatalf("leftovers: %v", got)
		}
		if es, _ := os.ReadDir(dir); len(es) != 2 || es[0].Name() != "blueprint.json" || es[1].Name() != "plan.md" {
			t.Fatalf("plan folder: %v", es)
		}
		if n := r.repoFiles(); n != 0 {
			t.Fatalf("%d files written in the repository", n)
		}
		text, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatal(err)
		}
		b, found := Parse(string(text))
		if len(found) != 0 || b.Plan != res.Plan || !uuid7.MatchString(b.Groups[0].ID) || b.Groups[0].ID == b.Groups[1].ID {
			t.Fatalf("plan.md: %s %+v", findingText(found), b)
		}
		stored, err := LoadBlueprint(r.home, "repo-1", res.Plan)
		if err != nil || stored.Repo != "repo-1" || stored.Plan != res.Plan {
			t.Fatalf("blueprint: %+v %v", stored, err)
		}
		for _, g := range stored.Groups {
			want, _ := CardHash(string(text), g)
			if res.Hashes[g.Name] != want || g.Hash != want || want == "" {
				t.Fatalf("hash of %s: %q %q want %q", g.Name, res.Hashes[g.Name], g.Hash, want)
			}
		}
		if _, found, err := Approve(r.home, "repo-1", res.Plan, nil, nil); err != nil || len(fails(found)) != 0 {
			t.Fatalf("approve: %v %s", err, findingText(found))
		}
		if r.ctx.req.Agent != "planner" || r.ctx.req.Slots[contextmgr.Goal] != "Add revocation" ||
			!strings.Contains(r.ctx.req.Slots[contextmgr.Specs], "the design") {
			t.Fatalf("request: %+v", r.ctx.req)
		}
		if len(r.sess.texts) != 1 || !strings.Contains(r.sess.texts[0], "BRIEF Add revocation") || r.sess.closed != 1 {
			t.Fatalf("session: %+v", r.sess)
		}
	})

	t.Run("a group id in the draft is kept", func(t *testing.T) {
		t.Parallel()
		g := bpGroup("tg-1")
		g.ID = uid(1)
		r := newRig(t, draftOfPlan(Blueprint{Goal: "Plan goal", Release: "V1", Groups: []PlanGroup{g}}))
		res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := LoadBlueprint(r.home, "repo-1", res.Plan); b.Groups[0].ID != uid(1) {
			t.Fatalf("id %q", b.Groups[0].ID)
		}
	})

	t.Run("a failing draft writes nothing and the retry gets the finding text", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, badDraft(), twoGroupDraft())
		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
			t.Fatal(err)
		}
		if len(r.sess.texts) != 2 || !strings.Contains(r.sess.texts[1], "plan.md#tg-1/tsk-one") ||
			!strings.Contains(r.sess.texts[1], "ears") {
			t.Fatalf("texts: %q", r.sess.texts)
		}
		if r.sess.started != 1 {
			t.Fatalf("sessions: %d", r.sess.started)
		}
	})

	t.Run("the third failure blocks, saves the draft and writes no plan", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, badDraft(), badDraft(), badDraft())
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var blocked PlanBlockedError
		if !errors.As(err, &blocked) || len(blocked.Findings) == 0 || !strings.Contains(blocked.Error(), "finding") {
			t.Fatalf("err: %v", err)
		}
		if len(r.sess.texts) != 3 || r.sess.closed != 1 {
			t.Fatalf("sends: %d closed %d", len(r.sess.texts), r.sess.closed)
		}
		b, err := os.ReadFile(filepath.Join(blocked.DraftPath, "plan.md"))
		if err != nil || string(b) != badDraft().Files[0].Content {
			t.Fatalf("saved draft: %v", err)
		}
		if filepath.Dir(blocked.DraftPath) != r.plans() ||
			!strings.HasPrefix(filepath.Base(blocked.DraftPath), ".plan-failed-") {
			t.Fatalf("draft path: %s", blocked.DraftPath)
		}
		if n := r.repoFiles(); n != 0 {
			t.Fatalf("%d files written in the repository", n)
		}
	})

	t.Run("a blocked draft with an unsafe path stays inside its folder", func(t *testing.T) {
		t.Parallel()
		d := badDraft()
		d.Files = append(d.Files, DraftFile{Path: "../../escape.md", Content: "x"}, DraftFile{Path: "/", Content: "y"})
		r := newRig(t, d, d, d)
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var blocked PlanBlockedError
		if !errors.As(err, &blocked) {
			t.Fatalf("err: %v", err)
		}
		if _, err := os.Stat(filepath.Join(r.home, "plans", "escape.md")); !os.IsNotExist(err) {
			t.Fatal("an unsafe draft path escaped the folder")
		}
	})

	t.Run("a draft that cannot be saved still blocks the plan", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, badDraft(), badDraft(), badDraft())
		if err := os.MkdirAll(r.plans(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(r.plans(), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(r.plans(), 0o755) })
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var blocked PlanBlockedError
		if !errors.As(err, &blocked) || blocked.DraftPath != "" {
			t.Fatalf("err: %v path %q", err, blocked.DraftPath)
		}
	})

	t.Run("the deadline ends the plan with no files", func(t *testing.T) {
		t.Parallel()
		block := func(ctx context.Context) (PlanDraft, error) { <-ctx.Done(); return PlanDraft{}, ctx.Err() }
		r := newRig(t, block)
		r.h.wallUnit = 20 * time.Millisecond
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err: %v", err)
		}
		if got := r.entries(); len(got) != 0 || r.sess.closed != 1 {
			t.Fatalf("entries %v closed %d", got, r.sess.closed)
		}
	})

	t.Run("input failures start no session", func(t *testing.T) {
		t.Parallel()
		cases := map[string]struct {
			req   PlanRequest
			setup func(*rig)
		}{
			"no goal and no document": {PlanRequest{}, nil},
			"missing document":        {PlanRequest{Docs: []string{"docs/none.md"}}, nil},
			"document outside":        {PlanRequest{Docs: []string{"../x.md"}}, nil},
			"context refused":         {PlanRequest{Goal: "g"}, func(r *rig) { r.ctx.err = contextmgr.ErrContext }},
			"tree unreadable": {PlanRequest{Goal: "g"}, func(r *rig) {
				r.h.d.Tree = func(context.Context) (contextmgr.Tree, error) { return nil, errors.New("no tree") }
			}},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := newRig(t, twoGroupDraft())
				if c.setup != nil {
					c.setup(r)
				}
				if _, err := r.h.Plan(context.Background(), c.req); err == nil {
					t.Fatal("want an error")
				}
				if r.sess.started != 0 || len(r.entries()) != 0 {
					t.Fatalf("session %d entries %v", r.sess.started, r.entries())
				}
			})
		}
	})

	t.Run("a session that cannot start or fails ends the plan", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, errors.New("model unreachable"))
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		if err == nil || !strings.Contains(err.Error(), "model unreachable") {
			t.Fatalf("err: %v", err)
		}
		if r.sess.closed != 1 || len(r.entries()) != 0 {
			t.Fatalf("closed %d entries %v", r.sess.closed, r.entries())
		}
		r = newRig(t)
		r.h.d.Start = func(context.Context) (PlannerSession, error) { return nil, errors.New("no session") }
		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err == nil ||
			!strings.Contains(err.Error(), "no session") {
			t.Fatalf("start err: %v", err)
		}
	})

	t.Run("a failed rename leaves no staging folder and no plan", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		r.h.rename = func(string, string) error { return errors.New("disk gone") }
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var w writeError
		if !errors.As(err, &w) || !strings.Contains(err.Error(), "disk gone") {
			t.Fatalf("err: %v", err)
		}
		if got := r.entries(); len(got) != 0 {
			t.Fatalf("entries: %v", got)
		}
	})

	t.Run("a home that cannot hold plans is a write failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		if err := os.WriteFile(filepath.Join(r.home, "plans"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var w writeError
		if !errors.As(err, &w) {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a plans folder that cannot take a staging folder is a write failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		if err := os.MkdirAll(r.plans(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(r.plans(), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(r.plans(), 0o755) })
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var w writeError
		if !errors.As(err, &w) {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("a repository id that leaves the plans folder is a write failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		r.h.d.Repo = "github.com/acme/shop"
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var w writeError
		if !errors.As(err, &w) || len(r.entries()) != 0 {
			t.Fatalf("err: %v", err)
		}
	})

	t.Run("the lint tool gives the findings LintDraft gives", func(t *testing.T) {
		t.Parallel()
		var fromTool []Finding
		var r *rig
		probe := func(context.Context) (PlanDraft, error) {
			fromTool = r.h.LintPlan(badDraft())
			return twoGroupDraft(), nil
		}
		r = newRig(t, probe)
		if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
			t.Fatal(err)
		}
		want := LintDraft(badDraft(), Blueprint{}, []string{".github/**"}, []string{"README.md", "docs/hld.md"})
		if len(want) == 0 || fmt.Sprint(fromTool) != fmt.Sprint(want) {
			t.Fatalf("tool %+v\nwant %+v", fromTool, want)
		}
		if got := r.h.LintPlan(badDraft()); len(got) == 0 {
			t.Fatal("the tool outside a plan found nothing")
		}
	})
}

func TestRestoreOrphans(t *testing.T) {
	t.Parallel()

	aside := func(t *testing.T, r *rig, name, plan string) {
		t.Helper()
		b := bpPlan(bpGroup("tg-1"))
		b.Plan, b.Repo = plan, "repo-1"
		if err := WriteBlueprint(r.home, b); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(PlanDir(r.home, "repo-1", plan), filepath.Join(r.plans(), name)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the next plan restores an old folder whose place is empty", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		aside(t, r, ".plan-old-x", "plan-aaaaaa")
		res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		if err != nil {
			t.Fatal(err)
		}
		got := r.entries()
		if len(got) != 2 || !slices.Contains(got, "plan-aaaaaa") || !slices.Contains(got, res.Plan) {
			t.Fatalf("entries: %v", got)
		}
		if _, err := LoadBlueprint(r.home, "repo-1", "plan-aaaaaa"); err != nil {
			t.Fatalf("old plan not restored: %v", err)
		}
	})

	t.Run("an old folder whose place is filled is dropped", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		aside(t, r, ".plan-old-y", "plan-bbbbbb")
		b := bpPlan(bpGroup("tg-1"))
		b.Plan, b.Repo = "plan-bbbbbb", "repo-1"
		b.Goal = "the new one"
		if err := WriteBlueprint(r.home, b); err != nil {
			t.Fatal(err)
		}
		if err := restoreOrphans(r.home, "repo-1"); err != nil {
			t.Fatal(err)
		}
		if got := r.entries(); !slices.Equal(got, []string{"plan-bbbbbb"}) {
			t.Fatalf("entries: %v", got)
		}
		if got, _ := LoadBlueprint(r.home, "repo-1", "plan-bbbbbb"); got.Goal != "the new one" {
			t.Fatalf("the new plan was overwritten: %+v", got)
		}
	})

	t.Run("the plan id comes from plan.md when blueprint.json is gone", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		dir := filepath.Join(r.plans(), ".plan-old-z")
		b := bpPlan(bpGroup("tg-1"))
		b.Plan = "plan-cccccc"
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(Render(b)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := restoreOrphans(r.home, "repo-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(r.plans(), "plan-cccccc", "plan.md")); err != nil {
			t.Fatalf("not restored: %v", err)
		}
	})

	t.Run("a folder with no readable plan id stays for a person", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		dir := filepath.Join(r.plans(), ".plan-old-q")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := restoreOrphans(r.home, "repo-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("folder removed: %v", err)
		}
	})

	t.Run("a missing plans folder is nothing to restore", func(t *testing.T) {
		t.Parallel()
		if err := restoreOrphans(t.TempDir(), "repo-1"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a rename that fails stops the plan", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, twoGroupDraft())
		aside(t, r, ".plan-old-x", "plan-aaaaaa")
		if err := os.Chmod(r.plans(), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(r.plans(), 0o755) })
		_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
		var w writeError
		if !errors.As(err, &w) {
			t.Fatalf("err: %v", err)
		}
	})
}

func TestPlannerRoleHoldsNoWriteTool(t *testing.T) {
	t.Parallel()
	for _, tool := range agent.PlannerRole().Tools {
		for _, bad := range []string{"write", "edit", "run", "git", "bash", "shell"} {
			if strings.Contains(strings.ToLower(tool), bad) {
				t.Fatalf("planner has tool %q", tool)
			}
		}
	}
}

func TestDraftFromResult(t *testing.T) {
	t.Parallel()

	t.Run("the plan text becomes plan.md and its parsed blueprint", func(t *testing.T) {
		t.Parallel()
		text := Render(Blueprint{Goal: "Plan goal", Release: "V1", Groups: []PlanGroup{bpGroup("tg-1")}})
		d := DraftFromResult(agent.PlannerResult{Plan: text})
		if len(d.Files) != 1 || d.Files[0] != (DraftFile{Path: "plan.md", Content: text}) {
			t.Fatalf("files: %+v", d.Files)
		}
		if d.Blueprint.Goal != "Plan goal" || len(d.Blueprint.Groups) != 1 || d.Blueprint.Groups[0].Name != "tg-1" {
			t.Fatalf("blueprint: %+v", d.Blueprint)
		}
	})

	t.Run("a plan with grammar problems still reaches lint", func(t *testing.T) {
		t.Parallel()
		d := DraftFromResult(agent.PlannerResult{Plan: "# Plan"})
		if len(fails(LintDraft(d, Blueprint{}, nil, nil))) == 0 {
			t.Fatal("lint passed a plan with no goal")
		}
	})
}

func TestDraftBlueprint(t *testing.T) {
	t.Parallel()

	t.Run("a draft with no parsed blueprint is parsed from plan.md", func(t *testing.T) {
		t.Parallel()
		if got := twoGroupDraft().blueprint(); len(got.Groups) != 2 {
			t.Fatalf("blueprint: %+v", got)
		}
	})

	t.Run("a draft with no plan.md has an empty blueprint", func(t *testing.T) {
		t.Parallel()
		if got := (PlanDraft{}).blueprint(); got.Goal != "" || len(got.Groups) != 0 {
			t.Fatalf("blueprint: %+v", got)
		}
	})
}

// newDraft renders groups as the draft of a new plan: no plan id and no group ids.
func newDraft(groups ...PlanGroup) PlanDraft {
	return draftOfPlan(Blueprint{Goal: "Plan goal", Release: "V1", Groups: groups})
}

// twoGroupDraft is a passing draft of two groups, the second waiting on the first.
func twoGroupDraft() PlanDraft { return newDraft(bpGroup("tg-1"), bpGroup("tg-2", "tg-1")) }

// badDraft is a draft whose acceptance item has no SHALL.
func badDraft() PlanDraft {
	g := bpGroup("tg-1")
	g.Tasks[0].Acceptance = []Acceptance{{Keyword: "WHEN", Text: "the system should work"}}
	return newDraft(g)
}
