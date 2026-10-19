package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// fakeProvider answers Probe and Profile from fixed values.
type fakeProvider struct {
	link   llm.Link
	probe  llm.ProbeResult
	perr   error
	prof   llm.CapacityProfile
	proerr error
	start  func(s llm.Session) (llm.Stream, error)
	resume func(s llm.Session, in llm.Input) (llm.Stream, error)
}

func (f *fakeProvider) Link() llm.Link { return f.link }

func (f *fakeProvider) Probe(context.Context) (llm.ProbeResult, error) {
	f.probe.Link = f.link
	return f.probe, f.perr
}

func (f *fakeProvider) Profile(context.Context, llm.Weight) (llm.CapacityProfile, error) {
	return f.prof, f.proerr
}

func (f *fakeProvider) Start(_ context.Context, s llm.Session) (llm.Stream, error) {
	if f.start == nil {
		return nil, llm.ErrUnavailable
	}
	return f.start(s)
}

func (f *fakeProvider) Resume(_ context.Context, s llm.Session, in llm.Input) (llm.Stream, error) {
	if f.resume == nil {
		return nil, llm.ErrUnavailable
	}
	return f.resume(s, in)
}

// fakeLimits is an in-memory LimitStore.
type fakeLimits struct {
	mu       sync.Mutex
	resets   map[llm.Link]time.Time
	slots    map[llm.Link]int
	full     map[llm.Link]bool
	released []llm.Link
}

var bothLinks = []llm.Link{llm.ClaudeCLI, llm.AnthropicAPI}

func newLimits() *fakeLimits {
	return &fakeLimits{resets: map[llm.Link]time.Time{}, slots: map[llm.Link]int{}, full: map[llm.Link]bool{}}
}

func (f *fakeLimits) ResetAt(l llm.Link) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.resets[l]
	return t, ok
}

func (f *fakeLimits) Record(l llm.Link, reset time.Time, _ llm.CapKind) error {
	f.mu.Lock()
	f.resets[l] = reset
	f.mu.Unlock()
	return nil
}

func (f *fakeLimits) TakeSlot(l llm.Link, max int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.full[l] || f.slots[l] >= max {
		return false, nil
	}
	f.slots[l]++
	return true, nil
}

func (f *fakeLimits) ReleaseSlot(l llm.Link) error {
	f.mu.Lock()
	f.slots[l]--
	f.released = append(f.released, l)
	f.mu.Unlock()
	return nil
}

func TestSelectPaidLinkUnderEachPolicy(t *testing.T) {
	ctx := context.Background()
	req := SelectRequest{Chain: []llm.Link{llm.AnthropicAPI, llm.ClaudeCLI}, Weight: llm.Standard, BriefTokens: 1000}
	// never: the paid link is removed, the subscription link serves.
	r := newRouter("never", newLimits(), subLink(llm.ClaudeCLI), paidLink(llm.AnthropicAPI))
	sel, err := r.Select(ctx, req)
	if err != nil || sel.Link != llm.ClaudeCLI {
		t.Fatalf("never: got %+v, %v", sel, err)
	}
	if len(sel.Skips) != 1 || sel.Skips[0].Reason != "paid" || sel.Skips[0].From != llm.AnthropicAPI ||
		sel.Skips[0].To != llm.ClaudeCLI {
		t.Fatalf("never: skips %+v", sel.Skips)
	}
	// fallback: the paid link runs only after every subscription link is skipped, whatever its chain place.
	r = newRouter("fallback", newLimits(), subLink(llm.ClaudeCLI), paidLink(llm.AnthropicAPI))
	if sel, err = r.Select(ctx, req); err != nil || sel.Link != llm.ClaudeCLI {
		t.Fatalf("fallback with a subscription link: got %+v, %v", sel, err)
	}
	lim := newLimits()
	lim.resets[llm.ClaudeCLI] = t0.Add(time.Hour)
	r = newRouter("fallback", lim, subLink(llm.ClaudeCLI), paidLink(llm.AnthropicAPI))
	if sel, err = r.Select(ctx, req); err != nil || sel.Link != llm.AnthropicAPI {
		t.Fatalf("fallback with the subscription limited: got %+v, %v", sel, err)
	}
	// allowed: the paid link runs in its chain position.
	r = newRouter("allowed", newLimits(), subLink(llm.ClaudeCLI), paidLink(llm.AnthropicAPI))
	if sel, err = r.Select(ctx, req); err != nil || sel.Link != llm.AnthropicAPI {
		t.Fatalf("allowed: got %+v, %v", sel, err)
	}
	// never with only a paid link left: paid_refused.
	r = newRouter("never", newLimits(), paidLink(llm.AnthropicAPI))
	if _, err = r.Select(ctx, SelectRequest{
		Chain:  []llm.Link{llm.AnthropicAPI},
		Weight: llm.Standard,
	}); !errors.Is(err, ErrPaidRefused) {
		t.Fatalf("never with only paid: %v", err)
	}
}

func TestSelectBriefOfExactlyHalfTheContextFits(t *testing.T) {
	ctx := context.Background()
	small, big := subLink(llm.ClaudeCLI), paidLink(llm.AnthropicAPI)
	small.prof.Ctx = 1000
	r := newRouter("allowed", newLimits(), small, big)
	sel, err := r.Select(ctx, SelectRequest{Chain: bothLinks, Weight: llm.Standard, BriefTokens: 500})
	if err != nil || sel.Link != llm.ClaudeCLI {
		t.Fatalf("a brief of exactly half: got %+v, %v", sel, err)
	}
	sel, err = r.Select(ctx, SelectRequest{Chain: bothLinks, Weight: llm.Standard, BriefTokens: 501})
	if err != nil || sel.Link != llm.AnthropicAPI || sel.Skips[0].Reason != "ctx" {
		t.Fatalf("one token more moves on: got %+v, %v", sel, err)
	}
	big.prof.Ctx = 1000
	if _, err = r.Select(ctx, SelectRequest{
		Chain:       bothLinks,
		Weight:      llm.Standard,
		BriefTokens: 501,
	}); !errors.Is(err, ErrContext) {
		t.Fatalf("every link too small: %v", err)
	}
}

func TestSelectSkipsLimitedLinksAndPausesWhenNoneIsLeft(t *testing.T) {
	ctx := context.Background()
	lim := newLimits()
	lim.resets[llm.ClaudeCLI] = t0.Add(2 * time.Hour)
	r := newRouter("never", lim, subLink(llm.ClaudeCLI))
	_, err := r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.ClaudeCLI}, Weight: llm.Standard})
	var pe *PausedError
	if !errors.As(err, &pe) || !errors.Is(err, ErrPaused) {
		t.Fatalf("every link limited: got %v", err)
	}
	if len(pe.Limits) != 1 || pe.Limits[0].Link != llm.ClaudeCLI || !pe.Limits[0].ResetAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("limits %+v", pe.Limits)
	}
	// A reset in the past no longer limits the link.
	lim.resets[llm.ClaudeCLI] = t0.Add(-time.Minute)
	if sel, err := r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.ClaudeCLI}, Weight: llm.Standard}); err != nil ||
		sel.Link != llm.ClaudeCLI {
		t.Fatalf("limit lifted: %+v, %v", sel, err)
	}
	// Excluded links (no free slot) pause too.
	if _, err := r.Select(ctx, SelectRequest{
		Chain:   []llm.Link{llm.ClaudeCLI},
		Weight:  llm.Standard,
		Exclude: []llm.Link{llm.ClaudeCLI},
	}); !errors.Is(err, ErrPaused) {
		t.Fatalf("excluded link: %v", err)
	}
}

func TestSelectChoosesBuiltOpenAILinks(t *testing.T) {
	ctx := context.Background()
	sub := subLink(llm.OpenAISubscription)
	sub.probe.Models = map[llm.Weight]string{llm.Standard: "gpt-small"}
	paid := paidLink(llm.OpenAIAPI)
	paid.probe.Models = map[llm.Weight]string{llm.Standard: "gpt-small"}

	r := newRouter("allowed", newLimits(), sub, paid)
	sel, err := r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.OpenAISubscription, llm.OpenAIAPI}, Weight: llm.Standard})
	if err != nil || sel.Link != llm.OpenAISubscription || sel.Model != "gpt-small" || len(sel.Skips) != 0 {
		t.Fatalf("subscription first: %+v, %v", sel, err)
	}

	lim := newLimits()
	lim.resets[llm.OpenAISubscription] = t0.Add(time.Hour)
	r = newRouter("allowed", lim, sub, paid)
	sel, err = r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.OpenAISubscription, llm.OpenAIAPI}, Weight: llm.Standard})
	if err != nil || sel.Link != llm.OpenAIAPI || sel.Profile.CostClass != llm.CostPaid {
		t.Fatalf("limited subscription falls to the paid link: %+v, %v", sel, err)
	}

	r = newRouter("never", newLimits(), paid)
	_, err = r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.OpenAIAPI}, Weight: llm.Standard})
	if !errors.Is(err, ErrPaidRefused) {
		t.Fatalf("the paid OpenAI link follows providers.paid: %v", err)
	}
}

func TestSelectSkipsUnbuiltUnavailableAndUnauthorizedLinks(t *testing.T) {
	ctx := context.Background()
	down := subLink(llm.AnthropicAPI)
	down.perr = llm.ErrUnavailable
	out := subLink(llm.Local)
	out.perr = llm.ErrUnauthorized
	unbuilt := subLink(llm.OpenAIAPI)
	unbuilt.perr = llm.ErrNotYet
	r := newRouter("allowed", newLimits(), down, out, unbuilt, subLink(llm.ClaudeCLI))
	sel, err := r.Select(ctx, SelectRequest{
		Chain:  []llm.Link{llm.OpenAISubscription, llm.OpenAIAPI, llm.AnthropicAPI, llm.Local, llm.ClaudeCLI},
		Weight: llm.Standard,
	})
	if err != nil || sel.Link != llm.ClaudeCLI {
		t.Fatalf("got %+v, %v", sel, err)
	}
	// the subscription link has no adapter on this machine; the other OpenAI link reports itself unbuilt
	want := []string{"unavailable", "not_built", "unavailable", "unauthorized"}
	if len(sel.Skips) != len(want) {
		t.Fatalf("skips %+v", sel.Skips)
	}
	for i, s := range sel.Skips {
		if s.Reason != want[i] {
			t.Fatalf("skip %d is %q, want %q", i, s.Reason, want[i])
		}
	}
	r = newRouter("allowed", newLimits(), down)
	if _, err = r.Select(ctx, SelectRequest{
		Chain:  []llm.Link{llm.AnthropicAPI},
		Weight: llm.Standard,
	}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("nothing usable: %v", err)
	}
}

func TestSelectLocalLinkBelowTheContextFloor(t *testing.T) {
	ctx := context.Background()
	local := subLink(llm.Local)
	local.prof = llm.CapacityProfile{Ctx: 4096, Slots: 1, CostClass: llm.CostFree}
	r := newRouter("never", newLimits(), local, subLink(llm.ClaudeCLI))
	r.CtxFloor = 262144
	sel, err := r.Select(ctx, SelectRequest{
		Chain:       []llm.Link{llm.Local, llm.ClaudeCLI},
		Weight:      llm.Standard,
		BriefTokens: 100,
	})
	if err != nil || sel.Link != llm.ClaudeCLI || sel.Skips[0].Reason != "ctx_below_floor" {
		t.Fatalf("got %+v, %v", sel, err)
	}
}

func TestSelectTakesTheModelFromTheProbeOrTheOverride(t *testing.T) {
	ctx := context.Background()
	r := newRouter("never", newLimits(), subLink(llm.ClaudeCLI))
	sel, _ := r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.ClaudeCLI}, Weight: llm.Standard})
	if sel.Model != "model-claude-cli" || sel.Profile.Ctx != 200000 {
		t.Fatalf("got %+v", sel)
	}
	sel, _ = r.Select(ctx, SelectRequest{Chain: []llm.Link{llm.ClaudeCLI}, Weight: llm.Standard, Model: "pinned-1"})
	if sel.Model != "pinned-1" {
		t.Fatalf("override: %+v", sel)
	}
}

func TestRecordLimitStoresTheResetAndDefaultsAnUnknownOne(t *testing.T) {
	lim := newLimits()
	r := newRouter("never", lim)
	reset := t0.Add(3 * time.Hour)
	if err := r.RecordLimit(context.Background(), &llm.ProviderLimit{
		Link:    llm.ClaudeCLI,
		Cap:     llm.CapWindow,
		ResetAt: reset,
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := lim.ResetAt(llm.ClaudeCLI); !ok || !got.Equal(reset) {
		t.Fatalf("stored %v, %v", got, ok)
	}
	if err := r.RecordLimit(context.Background(), &llm.ProviderLimit{
		Link: llm.AnthropicAPI,
		Cap:  llm.CapQuota,
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := lim.ResetAt(llm.AnthropicAPI); !got.Equal(t0.Add(15 * time.Minute)) {
		t.Fatalf("an unknown reset is stored as 15 minutes out, got %v", got)
	}
}

// paidLink is a healthy paid link.
func paidLink(l llm.Link) *fakeProvider {
	p := subLink(l)
	p.prof.CostClass = llm.CostPaid
	return p
}

// subLink is a healthy subscription link with a 200,000-token context.
func subLink(l llm.Link) *fakeProvider {
	return &fakeProvider{link: l, probe: llm.ProbeResult{
		OK:     true,
		Models: map[llm.Weight]string{llm.Standard: "model-" + string(l)},
	},
		prof: llm.CapacityProfile{Ctx: 200000, Slots: 3, CostClass: llm.CostSubscription, Effort: "medium"}}
}

// newRouter builds a router over fake providers at a fixed clock.
func newRouter(paid string, lim *fakeLimits, ps ...*fakeProvider) *Router {
	m := map[llm.Link]llm.Provider{}
	for _, p := range ps {
		m[p.link] = p
	}
	return &Router{Client: llm.New(m), Quota: lim, Paid: paid, Now: func() time.Time { return t0 }}
}
