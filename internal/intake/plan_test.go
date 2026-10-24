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
	runs map[string]string
	err  error
}

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

// rig is a handler wired to fakes over a temporary repository.
type rig struct {
	t    *testing.T
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
	r := &rig{t: t, root: t.TempDir(), bus: bus.New(bus.Options{}), ctx: &fakeContext{},
		sess: &scripted{
			steps: steps,
		}, tree: fakeTree{"README.md": "readme", "docs/hld.md": "the design"}, lock: fakeLocks{runs: map[string]string{}}}
	t.Cleanup(func() { _ = r.bus.Close() })
	r.h = New(Deps{
		Bus:       r.bus,
		Context:   r.ctx,
		Start:     r.sess.start,
		Locks:     r.lock,
		Protected: func() []string { return []string{".github/**"} },
		Limits:    PlannerLimits{WallMin: 1},
		Root:      r.root,
		Tree:      func(context.Context) (contextmgr.Tree, error) { return r.tree, nil },
		Repo:      "github.com/acme/shop", CommonDir: filepath.Join(r.root, ".git"),
	})
	return r
}

// entries lists the names directly under docs/backlog.
func (r *rig) entries() []string {
	es, _ := os.ReadDir(r.backlog(""))
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// backlog returns the path of a file under docs/backlog.
func (r *rig) backlog(rel string) string {
	return filepath.Join(r.root, "docs", "backlog", filepath.FromSlash(rel))
}

func TestPlanWritesAPassingDraft(t *testing.T) {
	r := newRig(t, twoGroupDraft())
	res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "Add revocation", Docs: []string{"docs/hld.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Epic != 1 || !slices.Equal(res.Groups, []string{"tg-1.1", "tg-1.2"}) {
		t.Fatalf("result: %+v", res)
	}
	if !slices.Contains(res.Written, "docs/backlog/epic-1/tg-1.2/TG.md") || len(res.Written) != 4 {
		t.Fatalf("written: %v", res.Written)
	}
	if b, err := os.ReadFile(r.backlog("epic-1/tg-1.1/TG.md")); err != nil || string(b) != twoGroupDraft().Files[0].Content {
		t.Fatalf("file: %v", err)
	}
	epics, fs := mustLoad(t, r.root)
	if len(fs) != 0 || len(epics) != 1 {
		t.Fatalf("reload: %+v %+v", epics, fs)
	}
	for _, g := range epics[0].Groups {
		want, _ := cardHashDir(r.root, g)
		if res.Hashes[g.Name] != want || want == "" {
			t.Fatalf("hash of %s: %q want %q", g.Name, res.Hashes[g.Name], want)
		}
	}
	if got := r.entries(); !slices.Equal(got, []string{"epic-1"}) {
		t.Fatalf("leftovers: %v", got)
	}
	if r.ctx.req.Agent != "planner" || r.ctx.req.Slots[contextmgr.Goal] != "Add revocation" ||
		!strings.Contains(r.ctx.req.Slots[contextmgr.Specs], "the design") {
		t.Fatalf("request: %+v", r.ctx.req)
	}
	if len(r.sess.texts) != 1 || !strings.Contains(r.sess.texts[0], "BRIEF Add revocation") || r.sess.closed != 1 {
		t.Fatalf("session: %+v", r.sess)
	}
}

func TestFailingDraftWritesNothingAndPassingRetryGetsTheText(t *testing.T) {
	r := newRig(t, badDraft(), twoGroupDraft())
	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if len(r.sess.texts) != 2 || !strings.Contains(r.sess.texts[1], "epic-1/tg-1.1/TG.md") ||
		!strings.Contains(r.sess.texts[1], "ears") {
		t.Fatalf("texts: %q", r.sess.texts)
	}
	if r.sess.started != 1 {
		t.Fatalf("sessions: %d", r.sess.started)
	}
}

func TestThirdFailureBlocksWithZeroFiles(t *testing.T) {
	r := newRig(t, badDraft(), badDraft(), badDraft())
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
	var blocked PlanBlockedError
	if !errors.As(err, &blocked) || len(blocked.Findings) == 0 {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(blocked.Error(), "finding") {
		t.Fatalf("message: %q", blocked.Error())
	}
	if len(r.sess.texts) != 3 || r.sess.closed != 1 {
		t.Fatalf("sends: %d closed %d", len(r.sess.texts), r.sess.closed)
	}
	if _, err := os.Stat(r.backlog("epic-1")); !os.IsNotExist(err) {
		t.Fatal("a blocked plan wrote an epic")
	}
	b, err := os.ReadFile(filepath.Join(blocked.DraftPath, "epic-1", "tg-1.1", "TG.md"))
	if err != nil || string(b) != badDraft().Files[0].Content {
		t.Fatalf("saved draft: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(blocked.DraftPath), ".plan-failed-") {
		t.Fatalf("draft path: %s", blocked.DraftPath)
	}
	// Load ignores the saved draft.
	if epics, fs := mustLoad(t, r.root); len(epics) != 0 || len(fs) != 0 {
		t.Fatalf("load saw the failed draft: %+v %+v", epics, fs)
	}
}

func TestBlockedDraftWithUnsafePathStaysInsideItsFolder(t *testing.T) {
	d := badDraft()
	d.Files = append(d.Files, DraftFile{Path: "../../escape.md", Content: "x"})
	r := newRig(t, d, d, d)
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
	var blocked PlanBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "escape.md")); !os.IsNotExist(err) {
		t.Fatal("an unsafe draft path escaped the folder")
	}
}

func TestDeadlineEndsThePlanWithZeroFiles(t *testing.T) {
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
}

func TestPlanInputFailuresStartNoSession(t *testing.T) {
	cases := map[string]struct {
		req   PlanRequest
		setup func(*rig)
	}{
		"no goal and no document": {PlanRequest{}, nil},
		"missing document":        {PlanRequest{Docs: []string{"docs/none.md"}}, nil},
		"document outside":        {PlanRequest{Docs: []string{"../x.md"}}, nil},
		"unknown epic":            {PlanRequest{Goal: "g", Epic: 4}, nil},
		"context refused":         {PlanRequest{Goal: "g"}, func(r *rig) { r.ctx.err = contextmgr.ErrContext }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
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
}

func TestLockCheckFailsClosed(t *testing.T) {
	r := newRig(t, twoGroupDraft())
	writeBacklog(t, r.root, goodGroup(1, 1, tgSpec{}))
	r.lock.err = errors.New("state unreadable")
	r.h.d.Locks = r.lock
	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1}); err == nil || r.sess.started != 0 {
		t.Fatalf("err %v sessions %d", err, r.sess.started)
	}
}

func TestAmendedPlanNamingAGroupInARunIsLocked(t *testing.T) {
	old := goodGroup(1, 1, tgSpec{})
	r := newRig(t, draftOf(goodGroup(1, 1, tgSpec{goal: "Changed"})))
	writeBacklog(t, r.root, old)
	r.lock.runs[uid(101)] = "run-9"
	r.h.d.Locks = r.lock
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1})
	var locked LockedError
	if !errors.As(err, &locked) || locked.Run != "run-9" || !strings.Contains(locked.Error(), "run-9") {
		t.Fatalf("err: %v", err)
	}
	if b, _ := os.ReadFile(r.backlog("epic-1/tg-1.1/TG.md")); string(b) != old[0].Content {
		t.Fatal("a locked group changed")
	}
	if !strings.Contains(r.ctx.req.Slots[contextmgr.Plan], "run-9") {
		t.Fatalf("backlog slot: %q", r.ctx.req.Slots[contextmgr.Plan])
	}
}

func TestAmendedPlanReplacesAGroupAndAddsOne(t *testing.T) {
	keep := goodGroup(1, 2, tgSpec{})
	r := newRig(t, draftOf(goodGroup(1, 1, tgSpec{goal: "Changed"}), goodGroup(1, 3, tgSpec{deps: []string{"tg-1.2"}})))
	writeBacklog(t, r.root, cat(goodGroup(1, 1, tgSpec{}), keep))
	res, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Epic != 1 || !slices.Equal(res.Groups, []string{"tg-1.1", "tg-1.3"}) {
		t.Fatalf("result: %+v", res)
	}
	if b, _ := os.ReadFile(r.backlog("epic-1/tg-1.1/TG.md")); !strings.Contains(string(b), "goal: Changed") {
		t.Fatal("amended group not replaced")
	}
	if b, _ := os.ReadFile(r.backlog("epic-1/tg-1.2/TG.md")); string(b) != keep[0].Content {
		t.Fatal("untouched group changed")
	}
	if got := r.entries(); !slices.Equal(got, []string{"epic-1"}) {
		t.Fatalf("leftovers: %v", got)
	}
}

func TestEpicNumberRules(t *testing.T) {
	cases := map[string]struct {
		req  PlanRequest
		have []DraftFile
		draf PlanDraft
	}{
		"new epic must be highest plus one": {
			PlanRequest{Goal: "g"},
			goodGroup(1, 1, tgSpec{}),
			draftOf(goodGroup(3, 1, tgSpec{})),
		},
		"new epic may not reuse a number": {
			PlanRequest{Goal: "g"},
			goodGroup(1, 1, tgSpec{}),
			draftOf(goodGroup(1, 2, tgSpec{})),
		},
		"amend stays in its epic": {
			PlanRequest{Goal: "g", Epic: 1},
			goodGroup(1, 1, tgSpec{}),
			draftOf(goodGroup(2, 1, tgSpec{})),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, c.draf, c.draf, c.draf)
			writeBacklog(t, r.root, c.have)
			_, err := r.h.Plan(context.Background(), c.req)
			var blocked PlanBlockedError
			if !errors.As(err, &blocked) || len(rulesOf(blocked.Findings, RuleGrammar, SeverityFail)) == 0 {
				t.Fatalf("err: %v", err)
			}
		})
	}
}

func TestRenameFailureRestoresTheOldGroup(t *testing.T) {
	old := goodGroup(1, 1, tgSpec{})
	r := newRig(t, draftOf(goodGroup(1, 1, tgSpec{goal: "Changed"})))
	writeBacklog(t, r.root, old)
	calls := 0
	r.h.rename = func(a, b string) error {
		calls++
		if calls == 2 {
			return errors.New("disk gone")
		}
		return os.Rename(a, b)
	}
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g", Epic: 1})
	if err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("err: %v", err)
	}
	if b, _ := os.ReadFile(r.backlog("epic-1/tg-1.1/TG.md")); string(b) != old[0].Content {
		t.Fatal("the old group was not restored")
	}
	if got := r.entries(); !slices.Equal(got, []string{"epic-1"}) {
		t.Fatalf("leftovers: %v", got)
	}
}

func TestNextPlanRestoresAnOrphanedOldFolder(t *testing.T) {
	old := goodGroup(1, 1, tgSpec{})
	r := newRig(t, draftOf(goodGroup(2, 1, tgSpec{})))
	// A crash between the two renames: the old group sits aside, its place is empty.
	aside := []DraftFile{
		{Path: ".plan-old-x/" + old[0].Path, Content: old[0].Content},
		{Path: ".plan-old-x/" + old[1].Path, Content: old[1].Content},
	}
	writeBacklog(t, r.root, aside)
	// A second crash after the new group went into place: the old copy is dropped.
	writeBacklog(t, r.root, goodGroup(1, 2, tgSpec{goal: "New"}))
	writeBacklog(t, r.root, []DraftFile{{Path: ".plan-old-y/epic-1/tg-1.2/TG.md", Content: "stale"}})
	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(r.backlog("epic-1/tg-1.1/TG.md")); err != nil || string(b) != old[0].Content {
		t.Fatalf("old group not restored: %v", err)
	}
	if b, _ := os.ReadFile(r.backlog("epic-1/tg-1.2/TG.md")); !strings.Contains(string(b), "goal: New") {
		t.Fatal("the new group was overwritten by the stale copy")
	}
	for _, n := range r.entries() {
		if strings.HasPrefix(n, ".plan-old-") {
			t.Fatalf("left %s", n)
		}
	}
}

func TestSessionErrorFailsThePlan(t *testing.T) {
	r := newRig(t, errors.New("model unreachable"))
	_, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"})
	if err == nil || !strings.Contains(err.Error(), "model unreachable") {
		t.Fatalf("err: %v", err)
	}
	if r.sess.closed != 1 || len(r.entries()) != 0 {
		t.Fatalf("closed %d entries %v", r.sess.closed, r.entries())
	}
}

func TestPlanLintToolMatcheslintDraftBacklog(t *testing.T) {
	var fromTool []Finding
	var r *rig
	probe := func(context.Context) (PlanDraft, error) {
		fromTool = r.h.LintBacklog(badDraft())
		return twoGroupDraft(), nil
	}
	r = newRig(t, probe)
	if _, err := r.h.Plan(context.Background(), PlanRequest{Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	want := lintDraftBacklog(badDraft(), nil, []string{".github/**"}, []string{"README.md", "docs/hld.md"})
	if len(want) == 0 || fmt.Sprint(fromTool) != fmt.Sprint(want) {
		t.Fatalf("tool %+v\nwant %+v", fromTool, want)
	}
}

func TestPlannerRoleHoldsNoWriteTool(t *testing.T) {
	for _, tool := range agent.PlannerRole().Tools {
		for _, bad := range []string{"write", "edit", "run", "git", "bash", "shell"} {
			if strings.Contains(strings.ToLower(tool), bad) {
				t.Fatalf("planner has tool %q", tool)
			}
		}
	}
}

func TestDraftFromResultHoldsThePlanText(t *testing.T) {
	d := DraftFromResult(agent.PlannerResult{Plan: "# Plan"})
	if len(d.Files) != 1 || d.Files[0] != (DraftFile{Path: "plan.md", Content: "# Plan"}) {
		t.Fatalf("draft: %+v", d)
	}
}

func twoGroupDraft() PlanDraft {
	return draftOf(goodGroup(1, 1, tgSpec{}), goodGroup(1, 2, tgSpec{deps: []string{"tg-1.1"}}))
}

func badDraft() PlanDraft {
	return draftOf(goodGroup(1, 1, tgSpec{items: []string{"the system should work"}}))
}
