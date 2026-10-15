package resource

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
)

// calls is the ordered log of calls the fakes received.
type calls struct {
	mu  sync.Mutex
	log []string
}

// add appends one entry.
func (c *calls) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, s)
}

// all returns a copy of the log.
func (c *calls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.log...)
}

// fakeThrower stands in for the Error Handler and fails its first failN throws.
type fakeThrower struct {
	c       *calls
	mu      sync.Mutex
	thrown  []fault.ResourceError
	reports []error
	failN   int
}

// ThrowResourceError records e unless a failure is queued.
func (f *fakeThrower) ThrowResourceError(_ context.Context, e fault.ResourceError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.c.add("throw:" + e.Kind)
	if f.failN > 0 {
		f.failN--
		return errors.New("handler down")
	}
	f.thrown = append(f.thrown, e)
	return nil
}

// Report records an ordinary error.
func (f *fakeThrower) Report(_ fault.Source, _ string, _ fault.Kind, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, err)
}

// fakePauser stands in for the Runtime Manager and fails its first failPause pauses and failNudge nudges.
type fakePauser struct {
	c         *calls
	mu        sync.Mutex
	pauses    []PauseOrder
	nudges    []NudgeOrder
	failPause int
	failNudge int
	got       chan struct{}
}

// PauseGroups records o unless a failure is queued.
func (f *fakePauser) PauseGroups(_ context.Context, o PauseOrder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.c.add("pause:" + o.Reason)
	if f.failPause > 0 {
		f.failPause--
		return errors.New("runtime down")
	}
	f.pauses = append(f.pauses, o)
	if f.got != nil {
		f.got <- struct{}{}
	}
	return nil
}

// NudgeGroups records o unless a failure is queued.
func (f *fakePauser) NudgeGroups(_ context.Context, o NudgeOrder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.c.add("nudge")
	if f.failNudge > 0 {
		f.failNudge--
		return errors.New("runtime down")
	}
	f.nudges = append(f.nudges, o)
	return nil
}

// fakeRecords stands in for the State Manager.
type fakeRecords struct {
	mu       sync.Mutex
	recs     map[WindowKey]WindowRecord
	saves    int
	failSave int
}

// LoadWindows returns a copy of the stored records.
func (f *fakeRecords) LoadWindows(context.Context) (map[WindowKey]WindowRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[WindowKey]WindowRecord{}
	for k, v := range f.recs {
		out[k] = v
	}
	return out, nil
}

// SaveWindow stores r unless a failure is queued.
func (f *fakeRecords) SaveWindow(_ context.Context, k WindowKey, r WindowRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSave > 0 {
		f.failSave--
		return errors.New("disk full")
	}
	f.saves++
	f.recs[k] = r
	return nil
}

// rig is a Manager wired to fakes and a fixed clock.
type rig struct {
	m     *Manager
	th    *fakeThrower
	rt    *fakePauser
	st    *fakeRecords
	c     *calls
	clock time.Time
}

// newRig builds a rig over the given thresholds.
func newRig(th Thresholds) *rig {
	c := &calls{}
	r := &rig{th: &fakeThrower{c: c}, rt: &fakePauser{c: c}, st: &fakeRecords{recs: map[WindowKey]WindowRecord{}}, c: c, clock: base}
	r.m = New(th, Deps{Errors: r.th, Runtime: r.rt, State: r.st, Now: func() time.Time { return r.clock }})
	return r
}

// observe feeds e to the manager and returns its error.
func (r *rig) observe(e bus.Event) error { return r.m.Observe(context.Background(), e) }

// TestLimitEventThrowsErrorOnly: a limit event throws one error with the reset time and orders no pause.
func TestLimitEventThrowsErrorOnly(t *testing.T) {
	r := newRig(defaults)
	reset := base.Add(time.Hour)
	if err := r.observe(ev(subtypeLimit, "r1", "g1", &Limit{Link: "claude-cli", Kind: "window", ResetAt: reset})); err != nil {
		t.Fatal(err)
	}
	if len(r.th.thrown) != 1 || r.th.thrown[0].Severity != fault.SeverityError || !r.th.thrown[0].ResetAt.Equal(reset) {
		t.Fatalf("thrown %+v", r.th.thrown)
	}
	if len(r.rt.pauses) != 0 {
		t.Fatalf("pauses %+v", r.rt.pauses)
	}
}

// TestFullWindowThrowsThenPauses: 100 percent throws once and then pauses once, the groups seen on the link.
func TestFullWindowThrowsThenPauses(t *testing.T) {
	r := newRig(defaults)
	reset := base.Add(time.Hour)
	for _, g := range []string{"g2", "g1"} {
		if err := r.observe(head("claude-cli", "r1", g, 10, reset)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.observe(head("claude-cli", "r1", "g1", 100, reset)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"throw:window_full", "pause:window_full"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
	want := PauseOrder{Reason: "window_full", Link: "claude-cli", Groups: []string{"g1", "g2"}, Until: reset}
	if !reflect.DeepEqual(r.rt.pauses, []PauseOrder{want}) {
		t.Fatalf("pauses %+v, want %+v", r.rt.pauses, want)
	}
	if err := r.observe(head("claude-cli", "r1", "g1", 100, reset)); err != nil || len(r.c.all()) != 2 {
		t.Fatalf("repeat: err %v calls %v", err, r.c.all())
	}
}

// TestThrowFailureRetried: a failed throw is tried again on the next event, then never again, and no pause goes out before it.
func TestThrowFailureRetried(t *testing.T) {
	r := newRig(defaults)
	r.th.failN = 1
	reset := base.Add(time.Hour)
	if err := r.observe(head("claude-cli", "r1", "g1", 100, reset)); err == nil {
		t.Fatal("want the throw failure returned")
	}
	if want := []string{"throw:window_full"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
	for range 2 {
		if err := r.observe(head("claude-cli", "r1", "g1", 100, reset)); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"throw:window_full", "throw:window_full", "pause:window_full"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
}

// TestPauseFailureRetried: a failed pause is sent again on the next event, the error is not thrown twice, and it stops after success.
func TestPauseFailureRetried(t *testing.T) {
	r := newRig(defaults)
	r.rt.failPause = 1
	reset := base.Add(time.Hour)
	if err := r.observe(head("claude-cli", "r1", "g1", 100, reset)); err == nil {
		t.Fatal("want the pause failure returned")
	}
	for range 2 {
		if err := r.observe(head("claude-cli", "r1", "g1", 100, reset)); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"throw:window_full", "pause:window_full", "pause:window_full"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
}

// TestNudgeFailureRetried: a failed nudge is sent again on the next usage event, then not again.
func TestNudgeFailureRetried(t *testing.T) {
	r := newRig(defaults)
	r.rt.failNudge = 1
	if err := r.observe(toks("claude-cli", "r1", "g1", 1_800_000)); err == nil {
		t.Fatal("want the nudge failure returned")
	}
	for range 2 {
		if err := r.observe(toks("claude-cli", "r1", "g1", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"nudge", "nudge"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
}

// TestGroupCapThroughManager: reaching the cap throws and pauses, and a grant lets the group run to twice the cap.
func TestGroupCapThroughManager(t *testing.T) {
	r := newRig(defaults)
	if err := r.observe(toks("claude-cli", "r1", "g1", 2_000_000)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"throw:group_tokens", "pause:group_tokens"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
	r.m.GrantGroupTokens("g1")
	if err := r.observe(toks("claude-cli", "r1", "g1", 1_600_000)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"throw:group_tokens", "pause:group_tokens", "nudge"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
}

// TestWindowSavedAndReloaded: a reading saves the window, usage raises its session count, and a restart keeps only unreset windows.
func TestWindowSavedAndReloaded(t *testing.T) {
	r := newRig(defaults)
	reset := base.Add(time.Hour)
	if err := r.observe(head("claude-cli", "r1", "g1", 50, reset)); err != nil {
		t.Fatal(err)
	}
	k := WindowKey{Link: "claude-cli", Window: "five_hour"}
	if got := r.st.recs[k]; got.PercentUsed != 50 || !got.ResetAt.Equal(reset) || got.Sessions != 0 {
		t.Fatalf("record after the reading %+v", got)
	}
	for range 2 {
		if err := r.observe(ev(subtypeUsage, "r1", "g1", &Usage{Link: "claude-cli", TokensIn: 1})); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.st.recs[k]; got.Sessions != 2 {
		t.Fatalf("sessions %d, want 2", got.Sessions)
	}
	saves := r.st.saves
	if err := r.observe(head("claude-cli", "r1", "g1", 50, reset)); err != nil || r.st.saves != saves {
		t.Fatalf("an unchanged window was saved again: err %v saves %d, was %d", err, r.st.saves, saves)
	}

	r.st.recs[WindowKey{Link: "claude-cli", Window: "weekly"}] = WindowRecord{PercentUsed: 9, ResetAt: base.Add(-time.Minute)}
	again := newRig(defaults)
	again.st.recs = r.st.recs
	again.m.load(context.Background())
	w := again.m.st.Windows[k]
	if w == nil || w.PercentUsed != 50 || w.Sessions != 2 || !w.ResetAt.Equal(reset) {
		t.Fatalf("reloaded window %+v", w)
	}
	if len(again.m.st.Windows) != 1 {
		t.Fatalf("an expired window was loaded: %+v", again.m.st.Windows)
	}
}

// TestSaveFailureKeptInMemory: a failed save is reported, the window stays in memory and the next change saves it.
func TestSaveFailureKeptInMemory(t *testing.T) {
	r := newRig(defaults)
	r.st.failSave = 1
	reset := base.Add(time.Hour)
	if err := r.observe(head("claude-cli", "r1", "g1", 50, reset)); err == nil || len(r.th.reports) != 1 {
		t.Fatalf("want the save failure returned and reported: err %v reports %v", err, r.th.reports)
	}
	k := WindowKey{Link: "claude-cli", Window: "five_hour"}
	if r.m.st.Windows[k] == nil || len(r.st.recs) != 0 {
		t.Fatalf("window %+v records %+v", r.m.st.Windows[k], r.st.recs)
	}
	if err := r.observe(head("claude-cli", "r1", "g1", 51, reset)); err != nil || r.st.recs[k].PercentUsed != 51 {
		t.Fatalf("next change: err %v record %+v", err, r.st.recs[k])
	}
}

// TestOverageOnceThroughManager: an overage event throws one warning and a second throws none until the reset.
func TestOverageOnceThroughManager(t *testing.T) {
	r := newRig(defaults)
	reset := base.Add(time.Hour)
	e := ev(subtypeOverage, "r1", "g1", &Overage{Link: "claude-cli", ResetAt: reset})
	for range 2 {
		if err := r.observe(e); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"throw:overage"}; !reflect.DeepEqual(r.c.all(), want) {
		t.Fatalf("calls %v, want %v", r.c.all(), want)
	}
	r.clock = base.Add(2 * time.Hour)
	if err := r.observe(ev(subtypeOverage, "r1", "g1", &Overage{Link: "claude-cli", ResetAt: r.clock.Add(time.Hour)})); err != nil {
		t.Fatal(err)
	}
	if len(r.c.all()) != 2 {
		t.Fatalf("calls %v, want a second overage warning after the reset", r.c.all())
	}
}

// TestMalformedEventChangesNothing: a malformed event returns an error, reports it and leaves the totals as they were.
func TestMalformedEventChangesNothing(t *testing.T) {
	r := newRig(defaults)
	if err := r.observe(paid("anthropic-api", "r1", "g1", 1)); err != nil {
		t.Fatal(err)
	}
	before := r.m.st.Spend.RunUSD["r1"]
	for _, bad := range []bus.Event{
		ev(subtypeUsage, "r1", "g1", &Usage{Link: "", USD: 3}),
		ev(subtypeUsage, "r1", "g1", &Usage{Link: "anthropic-api", USD: -3}),
		ev(subtypeHeadroom, "r1", "g1", &Usage{Link: "anthropic-api", USD: 3}),
		{Type: bus.TypeResource, Subtype: subtypeLimit, Run: "r1"},
	} {
		if err := r.observe(bad); !errors.Is(err, errMalformed) {
			t.Fatalf("%+v: err %v, want errMalformed", bad, err)
		}
	}
	if got := r.m.st.Spend.RunUSD["r1"]; got != before || len(r.th.reports) != 4 {
		t.Fatalf("run spend %v, was %v; reports %d", got, before, len(r.th.reports))
	}
}

// TestUnknownSubtypeIgnored: an event of a subtype the manager does not read is ignored without an error.
func TestUnknownSubtypeIgnored(t *testing.T) {
	r := newRig(defaults)
	if err := r.observe(ev("limit.warn", "r1", "g1", &bus.LimitWarn{Link: "claude-cli", Kind: "quota"})); err != nil {
		t.Fatal(err)
	}
	if len(r.th.reports) != 0 || len(r.c.all()) != 0 {
		t.Fatalf("reports %v calls %v", r.th.reports, r.c.all())
	}
}

// TestRunServesTheBus: Run reads resource events from a real bus, survives a malformed event, orders the pause and ends with the context.
func TestRunServesTheBus(t *testing.T) {
	b := bus.New(bus.Options{})
	defer b.Close()
	r := newRig(defaults)
	r.rt.got = make(chan struct{}, 1)
	r.m.d.Bus = b
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.m.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	// Events published before Run subscribes are lost, so both are repeated until the pause arrives.
	for pending := true; pending; {
		for _, e := range []bus.Event{
			ev(subtypeUsage, "r1", "g1", &Usage{Link: "", USD: 1}),
			head("claude-cli", "r1", "g1", 100, time.Now().Add(time.Hour)),
		} {
			if err := b.Publish(e); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-r.rt.got:
			pending = false
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("no pause order arrived")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-deadline:
		t.Fatal("Run did not end with its context")
	}
	if len(r.th.reports) == 0 {
		t.Fatal("the malformed event was not reported")
	}
}

// TestEventsDecode: the example events decode through the bus registry into the four data structs.
func TestEventsDecode(t *testing.T) {
	line := `{"type":"resource","subtype":"usage","run":"r1","group":"g1","time":"2026-10-14T16:42:09Z","data":{"link":"anthropic-api","agent":"builder","session":"s1","model":"m","tokens_in":51200,"tokens_out":4300,"cache_read":40960,"cache_write":8192,"usd":0.31,"usd_equiv":0}}`
	var e bus.Event
	if err := e.UnmarshalJSON([]byte(line)); err != nil {
		t.Fatal(err)
	}
	u, ok := e.Data.(*Usage)
	if !ok || u.TokensIn != 51200 || u.USD != 0.31 {
		t.Fatalf("decoded %#v", e.Data)
	}
	for _, p := range []bus.Payload{&Headroom{}, &Overage{}, &Limit{}} {
		if k := p.EventKey(); k.Type != bus.TypeResource {
			t.Fatalf("%T bound to %v", p, k)
		}
	}
}

// The real Error Handler and Event Bus satisfy the interfaces the manager asks for.
var (
	_ Thrower    = (*fault.Handler)(nil)
	_ Subscriber = bus.Bus(nil)
)
