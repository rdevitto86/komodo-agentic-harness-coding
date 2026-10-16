package task

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// SelectRequest is what the pipeline asks for before each session.
type SelectRequest struct {
	Chain       []llm.Link
	Weight      llm.Weight
	Model       string // agent model override, empty for none
	BriefTokens int    // size of the assembled brief
	Exclude     []llm.Link
}

// Skip records one link passed over, published as the fact provider.skip.
type Skip struct {
	From, To llm.Link
	Reason   string // ctx_below_floor, ctx, quota, slots, unavailable, unauthorized, not_built or paid
}

// Selection is the chosen link and every skip on the way to it.
type Selection struct {
	Link    llm.Link
	Model   string
	Profile llm.CapacityProfile
	Skips   []Skip
}

// Router chooses links and records limits.
type Router struct {
	Client   *llm.Client
	Quota    LimitStore
	Paid     string       // providers.paid: never, fallback or allowed
	CtxFloor int          // providers.ctx_floor: smallest context the local link may have
	Catalog  *llm.Catalog // default and prior model identifiers; nil means none
	Now      func() time.Time
}

// Errors Select returns when no link can serve the session.
var (
	ErrNoProvider  = errors.New("every link unavailable or unauthorized")
	ErrPaidRefused = errors.New("only a paid link remains and providers.paid forbids it")
	ErrContext     = errors.New("the brief exceeds half the context of every link")
	ErrPaused      = errors.New("every remaining link is over its limit")
)

// PausedError carries the limits that emptied the chain; it unwraps to ErrPaused.
type PausedError struct{ Limits []*llm.ProviderLimit }

// Error describes the pause.
func (e *PausedError) Error() string { return ErrPaused.Error() }

// Unwrap returns ErrPaused.
func (e *PausedError) Unwrap() error { return ErrPaused }

// unknownReset is how long a limit without a reset time keeps its link out of every chain.
const unknownReset = 15 * time.Minute

// now returns the current time.
func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// walk is the state of one pass over a chain.
type walk struct {
	skips      []Skip
	limits     []*llm.ProviderLimit
	paidRefuse bool
	ctxFail    bool
	deferred   []llm.Link
}

// skip records one passed-over link; To is filled in when a link is chosen.
func (w *walk) skip(l llm.Link, reason string) { w.skips = append(w.skips, Skip{From: l, Reason: reason}) }

// Select returns the first link in the chain that can serve the session now.
// Under providers.paid fallback a paid link is tried only after every other link.
func (r *Router) Select(ctx context.Context, req SelectRequest) (Selection, error) {
	var w walk
	for _, l := range req.Chain {
		sel, ok, err := r.try(ctx, &w, l, req, false)
		if err != nil {
			return Selection{}, err
		}
		if ok {
			return r.chosen(sel, &w), nil
		}
	}
	for _, l := range w.deferred {
		sel, ok, err := r.try(ctx, &w, l, req, true)
		if err != nil {
			return Selection{}, err
		}
		if ok {
			return r.chosen(sel, &w), nil
		}
	}
	switch {
	case len(w.limits) > 0:
		return Selection{}, &PausedError{Limits: w.limits}
	case w.paidRefuse:
		return Selection{}, ErrPaidRefused
	case w.ctxFail:
		return Selection{}, ErrContext
	}
	return Selection{}, ErrNoProvider
}

// chosen stamps the destination on every skip.
func (r *Router) chosen(sel Selection, w *walk) Selection {
	for i := range w.skips {
		w.skips[i].To = sel.Link
	}
	sel.Skips = w.skips
	return sel
}

// try checks one link against the walk's rules and returns the selection when it can serve.
func (r *Router) try(ctx context.Context, w *walk, l llm.Link, req SelectRequest, paidNow bool) (Selection, bool, error) {
	if err := ctx.Err(); err != nil {
		return Selection{}, false, err
	}
	if !paidNow {
		if slices.Contains(req.Exclude, l) {
			w.skip(l, "slots")
			w.limits = append(w.limits, &llm.ProviderLimit{Link: l})
			return Selection{}, false, nil
		}
		if at, ok := r.Quota.ResetAt(l); ok && at.After(r.now()) {
			w.skip(l, "quota")
			w.limits = append(w.limits, &llm.ProviderLimit{Link: l, Cap: llm.CapWindow, ResetAt: at})
			return Selection{}, false, nil
		}
	}
	probe, err := r.Client.Probe(ctx, l)
	if err == nil && !probe.OK {
		err = llm.ErrUnavailable
	}
	var prof llm.CapacityProfile
	if err == nil {
		prof, err = r.Client.Profile(ctx, l, req.Weight)
	}
	if err != nil {
		w.skip(l, unusable(err))
		return Selection{}, false, nil
	}
	if prof.CostClass == llm.CostPaid && !paidNow {
		switch r.Paid {
		case "fallback":
			w.deferred = append(w.deferred, l)
			return Selection{}, false, nil
		case "allowed":
		default:
			w.skip(l, "paid")
			w.paidRefuse = true
			return Selection{}, false, nil
		}
	}
	if l == llm.Local && r.CtxFloor > 0 && prof.Ctx < r.CtxFloor {
		w.skip(l, "ctx_below_floor")
		return Selection{}, false, nil
	}
	if req.BriefTokens > prof.Ctx/2 {
		w.skip(l, "ctx")
		w.ctxFail = true
		return Selection{}, false, nil
	}
	model := req.Model
	if model == "" {
		model = probe.Models[req.Weight]
	}
	if model == "" && r.Catalog != nil {
		if e, ok := r.Catalog.Default(req.Weight); ok {
			model = e.ID
		}
	}
	if model == "" {
		w.skip(l, "unavailable")
		return Selection{}, false, nil
	}
	return Selection{Link: l, Model: model, Profile: prof}, true, nil
}

// unusable maps a probe or profile error to the skip reason.
func unusable(err error) string {
	switch {
	case errors.Is(err, llm.ErrNotYet):
		return "not_built"
	case errors.Is(err, llm.ErrUnauthorized):
		return "unauthorized"
	}
	return "unavailable"
}

// RecordLimit stores the link's reset time so every group skips it.
func (r *Router) RecordLimit(_ context.Context, e *llm.ProviderLimit) error {
	reset := e.ResetAt
	if reset.IsZero() {
		reset = r.now().Add(unknownReset)
	}
	return r.Quota.Record(e.Link, reset, e.Cap)
}
