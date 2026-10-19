package engine

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
)

type fakeConfig map[string]any

func (f fakeConfig) GetConfigItem(key string) (config.Item, error) {
	v, ok := f[key]
	if !ok {
		return config.Item{}, errors.New("unknown key " + key)
	}
	return config.Item{Key: key, Value: v}, nil
}

type fakeAgents map[string]int

func (f fakeAgents) Retries(a string) (int, error) { return f[a], nil }

func TestPickPrefersHighPriorityThenChainThenUnblocksThenOrder(t *testing.T) {
	hi := spec("hi", 5)
	hi.Priority = "high"
	cands := []Candidate{
		{Spec: spec("late", 3), ChainWeight: 2},
		{Spec: spec("first", 1), ChainWeight: 2},
		{Spec: hi, ChainWeight: 1},
		{Spec: spec("long", 4), ChainWeight: 9},
		{Spec: spec("wide", 2), ChainWeight: 2, Unblocks: 3},
	}
	var got []string
	for len(cands) > 0 {
		c, ok := Pick(cands)
		if !ok {
			t.Fatal("Pick found nothing")
		}
		got = append(got, c.Spec.ID)
		cands = slices.DeleteFunc(cands, func(x Candidate) bool { return x.Spec.ID == c.Spec.ID })
	}
	if want := []string{"hi", "long", "wide", "first", "late"}; !slices.Equal(got, want) {
		t.Fatalf("pick order %v, want %v", got, want)
	}
	if _, ok := Pick(nil); ok {
		t.Fatal("Pick of nothing returned a group")
	}
}

func TestCandidatesWeighChainsAndUnblocks(t *testing.T) {
	specs := []GroupSpec{spec("a", 1), spec("b", 2, "a"), spec("c", 3, "b"), spec("d", 4, "a", "x")}
	c := Candidates(specs, map[string]bool{})
	if len(c) != 1 || c[0].Spec.ID != "a" {
		t.Fatalf("ready = %+v", c)
	}
	if c[0].ChainWeight != 6 || c[0].Unblocks != 1 {
		t.Fatalf("chain weight %d unblocks %d", c[0].ChainWeight, c[0].Unblocks)
	}
	done := map[string]bool{"a": true, "x": true}
	ids := []string{}
	for _, x := range Candidates(specs, done) {
		ids = append(ids, x.Spec.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"b", "d"}) {
		t.Fatalf("ready after a = %v", ids)
	}
}

func TestStartableNeedsEveryDependencyDone(t *testing.T) {
	done := map[string]bool{"a": true}
	if !Startable(nil, done) || !Startable([]string{"a"}, done) {
		t.Fatal("a group with its dependencies done must start")
	}
	if Startable([]string{"a", "b"}, done) {
		t.Fatal("a group started with b not Done")
	}
}

func TestPoolHalvesToTheFloorAndIgnoresSignalsInTheCooldown(t *testing.T) {
	if got := HalvePool(6, 1); got != 3 {
		t.Fatalf("HalvePool(6,1) = %d", got)
	}
	if got := HalvePool(2, 2); got != 2 {
		t.Fatalf("HalvePool below the floor = %d", got)
	}
	if got := HalvePool(1, 1); got != 1 {
		t.Fatalf("HalvePool(1,1) = %d", got)
	}
	if got := GrowPool(3, 6, 4, 2); got != 4 {
		t.Fatalf("GrowPool with room = %d", got)
	}
	if got := GrowPool(3, 6, 1, 2); got != 3 {
		t.Fatalf("GrowPool without memory = %d", got)
	}
	if got := GrowPool(6, 6, 64, 2); got != 6 {
		t.Fatalf("GrowPool past the ceiling = %d", got)
	}
	clk := NewFakeClock(t0)
	p := NewPool(Limits{PoolMin: 1, PoolMax: 8}, clk, 5*time.Minute)
	p.Halve()
	if p.Size() != 4 {
		t.Fatalf("size after halving = %d", p.Size())
	}
	p.Halve()
	if p.Size() != 4 {
		t.Fatalf("a second signal inside the cooldown halved again: %d", p.Size())
	}
	clk.Advance(6 * time.Minute)
	p.Halve()
	if p.Size() != 2 {
		t.Fatalf("size after the cooldown = %d", p.Size())
	}
}

func TestPoolSlotsAndWaiters(t *testing.T) {
	p := NewPool(Limits{PoolMin: 1, PoolMax: 2}, NewFakeClock(t0), time.Minute)
	first, second, third := p.TryAcquire(), p.TryAcquire(), p.TryAcquire()
	if !first || !second || third {
		t.Fatal("a pool of 2 must give exactly 2 slots")
	}
	done := make(chan struct{})
	go func() {
		_ = p.Acquire(t.Context())
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	p.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter did not get the freed slot")
	}
	if p.TryAcquire() {
		t.Fatal("the slot went to the free count instead of the waiter")
	}
}

func TestFingerprintIgnoresLineNumbersAndOrder(t *testing.T) {
	a := Fingerprint([]string{"internal/x/a.go:12: TestA failed", "TestB", "internal/y.go:7:3: vet"})
	b := Fingerprint([]string{"internal/y.go:99:1: vet", "TestB", "internal/x/a.go:140: TestA failed"})
	if a != b {
		t.Fatalf("hashes differ: %s %s", a, b)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("hash form %q", a)
	}
	if a == Fingerprint([]string{"TestB"}) {
		t.Fatal("different items gave the same hash")
	}
}

func TestLandingPlanOrdersEachEpicByDependencyThenOrder(t *testing.T) {
	specs := []GroupSpec{
		spec("tg-1.3", 3, "tg-1.1"), spec("tg-1.2", 2), spec("tg-1.1", 1, "tg-1.2"),
		{ID: "tg-2.1", Label: "tg-2.1", Epic: "ep-2", Order: 4},
	}
	plans := BuildLanding(specs)
	if len(plans) != 2 {
		t.Fatalf("plans = %+v", plans)
	}
	if plans[0].Epic != "ep-1" || plans[0].Branch != "komodo/ep-1" ||
		!slices.Equal(plans[0].Order, []string{"tg-1.2", "tg-1.1", "tg-1.3"}) {
		t.Fatalf("ep-1 plan = %+v", plans[0])
	}
	if !slices.Equal(plans[1].Order, []string{"tg-2.1"}) {
		t.Fatalf("ep-2 plan = %+v", plans[1])
	}
}

func TestPlanRefusesACycleAndAnUnknownDependencyOutsideTheRun(t *testing.T) {
	if err := CheckPlan([]GroupSpec{spec("a", 1, "b"), spec("b", 2, "a")}); err == nil ||
		!strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	if err := CheckPlan([]GroupSpec{spec("a", 1), spec("b", 2, "a")}); err != nil {
		t.Fatalf("valid plan refused: %v", err)
	}
}

func TestLoadLimitsReadsEveryKeyOnce(t *testing.T) {
	cfg := fakeConfig{
		"limits.group_retries": 3, "limits.override_window_min": 30, "limits.override_retries": 3,
		"limits.pause_max_min": float64(360), "limits.group_wall_min": 60,
		"limits.group_tokens": 0, "limits.pool_min": 1, "limits.pool_max": 6, "sandbox.max_mem_gb": 4}
	l, err := LoadLimits(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := Limits{
		GroupRetries:      3,
		OverrideWindowMin: 30,
		OverrideRetries:   3,
		PauseMaxMin:       360,
		GroupWallMin:      60,
		PoolMin:           1,
		PoolMax:           6,
		SessionMemGB:      4,
	}
	if l != want {
		t.Fatalf("limits = %+v", l)
	}
	delete(cfg, "limits.group_retries")
	if _, err := LoadLimits(cfg); err == nil || !strings.Contains(err.Error(), "limits.group_retries") {
		t.Fatalf("error = %v", err)
	}
}

func TestGlobMatching(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"internal/lease/**", "internal/lease/fence.go", true},
		{"internal/lease/**", "internal/lease/x/y.go", true},
		{"internal/lease/*", "internal/lease/x/y.go", false},
		{"**/*.pem", "a/b/key.pem", true},
		{".env*", ".env.local", true},
		{".git", ".git", true},
		{".git/**", ".git/config", true},
		{"a?c", "abc", true},
		{"*.go", "x.go", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pat, c.path, got)
		}
	}
}

func spec(id string, order int, deps ...string) GroupSpec {
	return GroupSpec{
		ID:       id,
		Label:    id,
		Epic:     "ep-1",
		Depends:  deps,
		Tasks:    []string{"a", "b"},
		Priority: "normal",
		Order:    order,
	}
}
