package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// planStale is how long a plan folder stays unchanged before a sweep deletes it.
const planStale = 7 * 24 * time.Hour

// failedPrefix starts the name of a folder that holds a draft which failed lint.
const failedPrefix = ".plan-failed-"

// GroupStatus is where a plan's group stands in the group state.
type GroupStatus string

// The statuses of a plan's group.
const (
	StatusNone    GroupStatus = ""        // the group has no state yet
	StatusRunning GroupStatus = "running" // the group has state and is not closed
	StatusClosed  GroupStatus = "closed"  // the group is done
)

// Blueprints reads a plan's blueprint; the Intake Handler's LoadBlueprint backs it.
type Blueprints interface {
	// Labels returns the group labels of a plan, such as tg-1 and tg-2; a missing blueprint wraps os.ErrNotExist.
	Labels(ctx context.Context, plan string) ([]string, error)
}

// PlanGroups is implemented by a Groups that can read the status of one group of a plan.
type PlanGroups interface {
	PlanGroup(ctx context.Context, plan, label string) (GroupStatus, error)
}

// Merger tells whether a plan's pull request has merged; the Commit Handler's forge check backs it.
type Merger interface {
	// PlanMerged reports whether the pull request of the plan's epic branch has merged.
	PlanMerged(ctx context.Context, plan string) (bool, error)
}

// planState is what the group state says about a plan.
type planState struct {
	closed  bool // the plan has groups and every one is closed
	running bool // at least one group is not closed and has state
}

// sweepPlans deletes failed drafts, plan folders whose groups are all closed and whose pull request merged, and plan
// folders nothing runs in that have not changed for 7 days when stale is set; a folder with a running group is kept.
func (h *Handler) sweepPlans(ctx context.Context, rep *Report, dry, stale bool) {
	if h.Roots.Plans == "" {
		return
	}

	ents, err := os.ReadDir(h.Roots.Plans)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: h.Roots.Plans, Reason: err.Error()})
		return
	}

	for _, e := range ents {
		name := e.Name()
		if ctx.Err() != nil {
			return
		}
		if !e.IsDir() || !validName(name) {
			continue
		}

		dir := filepath.Join(h.Roots.Plans, name)
		switch {
		case strings.HasPrefix(name, failedPrefix):
			h.deletePlan(dir, rep, dry)
		case strings.HasPrefix(name, "."):
			continue // a staging folder belongs to the Intake Handler
		default:
			h.sweepPlan(ctx, rep, name, dir, dry, stale)
		}
	}
}

// sweepPlan decides one plan folder: closed plans go once their pull request merged, idle plans go when stale is set and
// they are old, the rest stay.
func (h *Handler) sweepPlan(ctx context.Context, rep *Report, plan, dir string, dry, stale bool) {
	var st planState
	if h.Plans != nil {
		var err error
		if st, err = h.planState(ctx, plan); err != nil {
			rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: dir, Reason: err.Error()})
			return
		}
	}

	old := stale && h.now().Sub(planChanged(dir)) >= planStale
	if st.running {
		rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: dir, Reason: "a group of the plan is running"})
		return
	}

	merged := false
	if st.closed && h.Merged != nil {
		var err error
		if merged, err = h.Merged.PlanMerged(ctx, plan); err != nil && !old {
			rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: dir, Reason: "check the pull request: " + err.Error()})
			return
		}
	}

	switch {
	case merged || old:
		h.deletePlan(dir, rep, dry)
	case st.closed:
		rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: dir, Reason: "the plan's pull request has not merged"})
	default:
		rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: dir, Reason: "the plan changed in the last 7 days"})
	}
}

// planState reads the plan's group labels and each group's status.
func (h *Handler) planState(ctx context.Context, plan string) (planState, error) {
	pg, ok := h.Groups.(PlanGroups)
	if !ok {
		return planState{}, errors.New("the group reader cannot tell a plan's group status")
	}

	labels, err := h.Plans.Labels(ctx, plan)
	if errors.Is(err, os.ErrNotExist) {
		return planState{}, nil // no blueprint, so no group can use the folder
	}
	if err != nil {
		return planState{}, fmt.Errorf("read the blueprint of %s: %w", plan, err)
	}

	st := planState{closed: len(labels) > 0}
	for _, l := range labels {
		s, err := pg.PlanGroup(ctx, plan, l)
		if err != nil {
			return planState{}, fmt.Errorf("read the status of %s %s: %w", plan, l, err)
		}
		if s != StatusClosed {
			st.closed = false
		}
		if s == StatusRunning {
			st.running = true
		}
	}
	return st, nil
}

// deletePlan removes a plan folder, or only lists it when dry.
func (h *Handler) deletePlan(dir string, rep *Report, dry bool) {
	if !dry {
		if _, err := removeInside(h.Roots.Plans, dir); err != nil {
			rep.Kept = append(rep.Kept, Item{Kind: KindPlan, Name: dir, Reason: err.Error()})
			h.escalate("", err)
			return
		}
	}
	rep.Removed = append(rep.Removed, Item{Kind: KindPlan, Name: dir})
}

// now reads the handler's clock.
func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// planChanged returns the newest change time of plan.md and blueprint.json, or of the folder when neither exists.
func planChanged(dir string) time.Time {
	var newest time.Time
	for _, f := range []string{"plan.md", "blueprint.json"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if newest.IsZero() {
		if fi, err := os.Stat(dir); err == nil {
			return fi.ModTime()
		}
	}
	return newest
}
