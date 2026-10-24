package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// planClock is the fixed time every plan test runs at.
var planClock = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// fakeBlueprints serves group labels per plan; a plan it lacks has no blueprint.
type fakeBlueprints struct {
	labels map[string][]string
	err    error
}

// Labels returns the configured labels, or os.ErrNotExist for an unknown plan.
func (f *fakeBlueprints) Labels(_ context.Context, plan string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	l, ok := f.labels[plan]
	if !ok {
		return nil, os.ErrNotExist
	}
	return l, nil
}

// fakePlanGroups adds group status to fakeGroups.
type fakePlanGroups struct {
	fakeGroups
	status map[string]GroupStatus // key is plan + "/" + label
	err    error
}

// PlanGroup returns the configured status; a group it lacks has none.
func (f *fakePlanGroups) PlanGroup(_ context.Context, plan, label string) (GroupStatus, error) {
	return f.status[plan+"/"+label], f.err
}

// planEnv is a handler wired to a plans folder, fake blueprints and a fixed clock.
type planEnv struct {
	*env
	bp *fakeBlueprints
	pg *fakePlanGroups
}

// newPlanEnv adds the plan wiring to a fresh fixture.
func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	e := newEnv(t)
	e.roots.Plans = filepath.Join(e.base, "home", "plans", "shop")
	if err := os.MkdirAll(e.roots.Plans, 0o755); err != nil {
		t.Fatal(err)
	}

	p := &planEnv{
		env: e,
		bp:  &fakeBlueprints{labels: map[string][]string{}},
		pg:  &fakePlanGroups{status: map[string]GroupStatus{}},
	}
	e.h.Roots = e.roots
	e.h.Plans = p.bp
	e.h.Groups = p.pg
	e.h.Now = func() time.Time { return planClock }
	return p
}

// plan makes a plan folder whose files are age old, with the given group labels.
func (p *planEnv) plan(id string, age time.Duration, labels ...string) string {
	p.t.Helper()
	dir := filepath.Join(p.roots.Plans, id)
	p.write(filepath.Join(dir, "plan.md"), "# plan\n")
	p.write(filepath.Join(dir, "blueprint.json"), "{}\n")
	when := planClock.Add(-age)
	for _, f := range []string{"plan.md", "blueprint.json"} {
		if err := os.Chtimes(filepath.Join(dir, f), when, when); err != nil {
			p.t.Fatal(err)
		}
	}
	p.bp.labels[id] = labels
	return dir
}

// day is 24 hours.
const day = 24 * time.Hour

func TestPlanFolders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a folder untouched for 8 days with no running group is removed", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-aaaaaa", 8*day, "tg-1")
		rep, err := p.h.Clean(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if exists(dir) || len(rep.Removed) != 1 || rep.Removed[0] != (Item{Kind: KindPlan, Name: dir}) {
			t.Fatalf("stale folder survives or is not listed: %+v", rep)
		}
	})

	t.Run("a folder untouched for 6 days is kept", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-bbbbbb", 6*day, "tg-1")
		rep, err := p.h.Clean(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !exists(dir) || len(rep.Kept) != 1 || rep.Kept[0].Kind != KindPlan {
			t.Fatalf("fresh folder was removed or not listed: %+v", rep)
		}
	})

	t.Run("a stale folder with a running group is kept", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-cccccc", 30*day, "tg-1", "tg-2")
		p.pg.status["plan-cccccc/tg-1"] = StatusClosed
		p.pg.status["plan-cccccc/tg-2"] = StatusRunning
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if !exists(dir) {
			t.Fatal("a plan with a running group was deleted")
		}
	})

	t.Run("a fresh folder whose groups are all closed is removed", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-dddddd", 0, "tg-1", "tg-2")
		p.pg.status["plan-dddddd/tg-1"] = StatusClosed
		p.pg.status["plan-dddddd/tg-2"] = StatusClosed
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(dir) {
			t.Fatal("a closed plan survives the sweep")
		}
	})

	t.Run("a failed draft goes and a staging folder stays", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		failed := filepath.Join(p.roots.Plans, ".plan-failed-ab12")
		staging := filepath.Join(p.roots.Plans, ".plan-ab12")
		p.write(filepath.Join(failed, "plan.md"), "x")
		p.write(filepath.Join(staging, "plan.md"), "x")
		p.write(filepath.Join(p.roots.Plans, "note.txt"), "x")
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(failed) || !exists(staging) {
			t.Fatalf("failed=%v staging=%v", exists(failed), exists(staging))
		}
	})

	t.Run("a folder with no blueprint ages out", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-eeeeee", 9*day)
		delete(p.bp.labels, "plan-eeeeee")
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(dir) {
			t.Fatal("an unreadable stale folder survives")
		}
	})

	t.Run("a folder without plan files ages by its own time", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := filepath.Join(p.roots.Plans, "plan-ffffff")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		old := planClock.Add(-10 * day)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(dir) {
			t.Fatal("an empty stale folder survives")
		}
	})

	t.Run("a nil clock reads the wall clock", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		p.h.Now = nil
		dir := p.plan("plan-gggggg", 0, "tg-1")
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if !exists(dir) {
			t.Fatal("a plan changed at the fixed clock's time was removed by the wall clock")
		}
	})
}

func TestPlanFoldersPreview(t *testing.T) {
	t.Parallel()
	p := newPlanEnv(t)
	stale := p.plan("plan-aaaaaa", 8*day, "tg-1")
	failed := filepath.Join(p.roots.Plans, ".plan-failed-x")
	p.write(filepath.Join(failed, "plan.md"), "x")

	rep, err := p.h.Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(stale) || !exists(failed) || len(rep.Removed) != 2 {
		t.Fatalf("preview deleted or missed a folder: %+v", rep)
	}
}

func TestPlanFoldersKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a status read error keeps the folder and says why", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-aaaaaa", 30*day, "tg-1")
		p.pg.err = errors.New("state down")
		rep, err := p.h.Clean(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !exists(dir) || len(rep.Kept) != 1 || rep.Kept[0].Reason == "" {
			t.Fatalf("folder lost or reason missing: %+v", rep)
		}
	})

	t.Run("a blueprint read error keeps the folder", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-aaaaaa", 30*day, "tg-1")
		p.bp.err = errors.New("bad json")
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if !exists(dir) {
			t.Fatal("a plan with an unreadable blueprint was deleted")
		}
	})

	t.Run("a group reader without status keeps the folder", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-aaaaaa", 30*day, "tg-1")
		p.h.Groups = &fakeGroups{}
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if !exists(dir) {
			t.Fatal("a plan was deleted without a status read")
		}
	})

	t.Run("no blueprint reader ages folders by time alone", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		p.h.Plans = nil
		stale := p.plan("plan-aaaaaa", 30*day, "tg-1")
		fresh := p.plan("plan-bbbbbb", day, "tg-1")
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(stale) || !exists(fresh) {
			t.Fatalf("stale=%v fresh=%v", exists(stale), exists(fresh))
		}
	})

	t.Run("no plans folder is a no-op", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		p.h.Roots.Plans = filepath.Join(p.base, "missing")
		if rep, err := p.h.Clean(ctx); err != nil || len(rep.Removed) != 0 {
			t.Fatalf("rep=%+v err=%v", rep, err)
		}
	})

	t.Run("an unreadable plans folder is listed", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		file := filepath.Join(p.base, "plans-file")
		p.write(file, "x")
		p.h.Roots.Plans = file
		rep, err := p.h.Clean(ctx)
		if err != nil || len(rep.Kept) != 1 || rep.Kept[0].Kind != KindPlan {
			t.Fatalf("rep=%+v err=%v", rep, err)
		}
	})
}

func TestPlanFoldersEngineStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("the engine-start sweep keeps an 8-day-old unused plan, clean removes it", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-aaaaaa", 8*day, "tg-1")
		if _, err := p.h.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if !exists(dir) {
			t.Fatal("the engine-start sweep deleted a stale plan")
		}
		if _, err := p.h.Clean(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(dir) {
			t.Fatal("clean kept a stale plan")
		}
	})

	t.Run("the engine-start sweep removes a finished plan and a failed draft", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		done := p.plan("plan-bbbbbb", 0, "tg-1")
		p.pg.status["plan-bbbbbb/tg-1"] = StatusClosed
		failed := filepath.Join(p.roots.Plans, ".plan-failed-ab12")
		p.write(filepath.Join(failed, "plan.md"), "x")
		if _, err := p.h.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if exists(done) || exists(failed) {
			t.Fatalf("done=%v failed=%v", exists(done), exists(failed))
		}
	})
}

func TestCompletePlanFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// complete lands one group of a two-group plan and returns the handler's report.
	complete := func(t *testing.T, p *planEnv, plan, label string) Report {
		t.Helper()
		g := p.addGroup("g1", "komodo/ep-1-g1")
		p.land(g)
		pub := p.published(g)
		pub.Plan, pub.Label = plan, label
		rep, err := p.h.Complete(ctx, pub)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	t.Run("the last group to close removes the plan folder", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-aaaaaa", 0, "tg-1", "tg-2")
		p.pg.status["plan-aaaaaa/tg-1"] = StatusClosed
		rep := complete(t, p, "plan-aaaaaa", "tg-2")
		if exists(dir) {
			t.Fatal("the plan folder survives its last group")
		}
		if got := rep.Removed[len(rep.Removed)-1]; got != (Item{Kind: KindPlan, Name: dir}) {
			t.Fatalf("plan not listed last: %+v", rep.Removed)
		}
	})

	t.Run("a plan with an open group is kept", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-bbbbbb", 0, "tg-1", "tg-2")
		p.pg.status["plan-bbbbbb/tg-1"] = StatusRunning
		rep := complete(t, p, "plan-bbbbbb", "tg-2")
		if !exists(dir) || len(rep.Kept) != 1 || rep.Kept[0].Kind != KindPlan {
			t.Fatalf("open plan removed or not listed: %+v", rep)
		}
	})

	t.Run("a fact without a plan touches no folder", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-cccccc", 0, "tg-1")
		complete(t, p, "", "")
		if !exists(dir) {
			t.Fatal("a plan folder went without a plan id")
		}
	})

	t.Run("a plan id that climbs out of the folder is refused", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		rep := complete(t, p, "../shop", "tg-1")
		if len(rep.Kept) != 1 || rep.Kept[0].Kind != KindPlan {
			t.Fatalf("bad plan id not refused: %+v", rep)
		}
	})

	t.Run("a status read error keeps the folder", func(t *testing.T) {
		t.Parallel()
		p := newPlanEnv(t)
		dir := p.plan("plan-dddddd", 0, "tg-1", "tg-2")
		p.pg.err = errors.New("state down")
		rep := complete(t, p, "plan-dddddd", "tg-2")
		if !exists(dir) || len(rep.Kept) != 1 {
			t.Fatalf("folder lost or error not listed: %+v", rep)
		}
	})
}
