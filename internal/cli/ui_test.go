package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

const (
	uiRunA = "0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57"
	uiRunB = "0192e4c5-7000-7a21-8c3b-2d9e0f1a4b58"
)

// uiFake is an engine that answers status and replay from fixtures and records every verb it gets.
type uiFake struct {
	mu     sync.Mutex
	verbs  []string
	events map[string][]bus.Event // replay answers by run id
	down   error                  // returned by Send when set
	hang   bool                   // Send blocks until the request's context ends
	empty  bool                   // status answers with no runs, queued entries or blocked groups
	stageB string                 // the stage of run B's group; commit when empty
	replay map[string]int         // replay requests by run id
}

// Ensure always succeeds.
func (*uiFake) Ensure(context.Context) error { return nil }

// Send answers a status or replay request from the fixtures.
func (f *uiFake) Send(ctx context.Context, r engine.Request) (<-chan engine.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verbs = append(f.verbs, r.Verb)
	if f.hang {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, ctx.Err()
	}
	if f.down != nil {
		return nil, f.down
	}
	ch := make(chan engine.Reply, 64)
	defer close(ch)
	switch r.Verb {
	case engine.VerbReplay:
		f.replay[r.Target]++
		for _, e := range f.events[r.Target] {
			ch <- engine.Reply{ID: r.ID, Status: engine.StatusEvent, Event: &e}
		}
		ch <- engine.Reply{ID: r.ID, Status: engine.StatusOK, Final: true}
	case engine.VerbStatus:
		if f.empty {
			ch <- okReply(map[string]any{"runs": []RunRow{}, "queued": []QueuedRow{}, "blocked": []BlockedRow{}})
			return ch, nil
		}
		d := map[string]any{
			"runs": []RunRow{
				{RunID: uiRunA, Target: "plan-7f3a9c", Groups: []GroupRow{
					{Group: "tg-1.1", Stage: "build", State: "running"},
					{Group: "tg-1.2", Stage: "test_review", State: "blocked"},
					{Group: "tg-1.3", Stage: "done", State: "done"},
				}},
				{RunID: uiRunB, Target: "tg-2.1", Groups: []GroupRow{{Group: "tg-2.1", Stage: f.stage(), State: "paused"}}},
			},
			"queued": []QueuedRow{{Target: "ep-3", RunID: "q1", At: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}},
			"blocked": []BlockedRow{
				{Group: "tg-1.2", RunID: uiRunA, Reason: "risky_change", Question: "Approve the change to the lease table?"},
			},
		}
		switch r.Target {
		case uiRunA:
			d["figures"] = Figures{WallS: 3725, TokensIn: 1000, TokensOut: 234, USD: 0.5, USDEquiv: 1.25}
		case uiRunB:
			d["figures"] = Figures{WallS: 60, TokensIn: 10, TokensOut: 5, USD: 0.25}
		}
		ch <- okReply(d)
	}
	return ch, nil
}

// stage returns run B's stage; the caller holds the lock.
func (f *uiFake) stage() string {
	if f.stageB == "" {
		return "commit"
	}
	return f.stageB
}

// moveB sets the stage of run B's group.
func (f *uiFake) moveB(stage string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stageB = stage
}

// replays returns how many replay requests a run got.
func (f *uiFake) replays(run string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replay[run]
}

// add appends an event to a run's replay.
func (f *uiFake) add(run string, e bus.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[run] = append(f.events[run], e)
}

// seenVerbs returns the verbs received so far.
func (f *uiFake) seenVerbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.verbs...)
}

// uiEvent builds a run-complete event at second s.
func uiEventAt(run string, s int, group string) bus.Event {
	return bus.Event{Type: bus.TypeRunComplete, Subtype: "stopped", Run: bus.ID(run), Group: bus.ID(group),
		Time: time.Date(2026, 10, 15, 12, 0, s, 0, time.UTC), Data: &bus.RunStopped{}}
}

func newUIFake() *uiFake {
	return &uiFake{replay: map[string]int{}, events: map[string][]bus.Event{
		uiRunA: {uiEventAt(uiRunA, 1, ""), uiEventAt(uiRunA, 3, "tg-1.1")},
		uiRunB: {uiEventAt(uiRunB, 2, "tg-2.1")},
	}}
}

// uiFixture starts the dashboard handler over a fake engine and returns its test server.
func uiFixture(t *testing.T, eng Engine, assets fs.FS) (*httptest.Server, *uiServer) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	s := newUIServer(
		uiDeps{Engine: eng, Root: "/repo", Assets: assets, Every: 10 * time.Millisecond},
		ts.Listener.Addr().String(),
	)
	ts.Config.Handler = s.Handler()
	ts.Start()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Poll(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; ts.Close() })
	return ts, s
}

// realAssets are the page files compiled into the binary.
func realAssets(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(komodo.Embedded, "embed/ui")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// getState fetches and decodes /state.
func getState(t *testing.T, ts *httptest.Server) uiState {
	t.Helper()
	resp, err := http.Get(ts.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var st uiState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUIStateMatchesStatusWithFigures(t *testing.T) {
	fake := newUIFake()
	ts, _ := uiFixture(t, fake, realAssets(t))
	st := getState(t, ts)
	if len(st.Runs) != 2 || len(st.Runs[0].Groups) != 3 || len(st.Queued) != 1 || len(st.Blocked) != 1 {
		t.Fatalf("runs/queued/blocked differ from status: %+v", st)
	}
	a := st.Runs[0]
	if a.Figures == nil || a.Figures.WallS != 3725 || a.Figures.TokensIn != 1000 || a.Figures.USD != 0.5 {
		t.Fatalf("run figures = %+v", a.Figures)
	}
	if g := a.Groups[1]; g.Question == "" || g.Reason != "risky_change" {
		t.Fatalf("a blocked group carries its question: %+v", g)
	}
	want := uiTotals{Running: 1, Paused: 1, Blocked: 1, Done: 1, Queued: 1, Tokens: 1249, USD: 0.75}
	if st.Totals != want {
		t.Fatalf("totals = %+v, want %+v", st.Totals, want)
	}
}

func TestUIFeedMergesRunEventsNewestFirst(t *testing.T) {
	ts, _ := uiFixture(t, newUIFake(), realAssets(t))
	st := getState(t, ts)
	if len(st.Events) != 3 || st.Events[0].Group != "tg-1.1" || st.Events[2].Group != "" {
		t.Fatalf("events = %+v", st.Events)
	}
	if e := st.Events[0]; e.Type != "run-complete" || e.Subtype != "stopped" || e.Run != uiRunA {
		t.Fatalf("event = %+v", e)
	}
}

func TestUIOnlyReadsFromTheEngine(t *testing.T) {
	fake := newUIFake()
	ts, _ := uiFixture(t, fake, realAssets(t))
	getState(t, ts)
	for _, v := range fake.seenVerbs() {
		if v != engine.VerbStatus && v != engine.VerbReplay {
			t.Fatalf("the dashboard sent the verb %q", v)
		}
	}
}

func TestUIEventsStreamPushesChanges(t *testing.T) {
	old := uiReplayEvery
	uiReplayEvery = 20 * time.Millisecond
	t.Cleanup(func() { uiReplayEvery = old })
	fake := newUIFake()
	ts, _ := uiFixture(t, fake, realAssets(t))
	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	rd := bufio.NewReader(resp.Body)
	read := func() uiState {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v", err)
			}
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				var st uiState
				if err := json.Unmarshal([]byte(rest), &st); err != nil {
					t.Fatal(err)
				}
				return st
			}
		}
	}
	if first := read(); len(first.Events) != 3 {
		t.Fatalf("first state has %d events", len(first.Events))
	}
	fake.add(uiRunB, uiEventAt(uiRunB, 9, "tg-2.1"))
	deadline := time.After(3 * time.Second)
	got := make(chan uiState, 1)
	go func() { got <- read() }()
	select {
	case st := <-got:
		if len(st.Events) != 4 || st.Events[0].Time.Second() != 9 {
			t.Fatalf("pushed state = %+v", st.Events)
		}
	case <-deadline:
		t.Fatal("no live event arrived within 3s")
	}
}

func TestUIRoutesAcceptNoStateChangingMethod(t *testing.T) {
	ts, _ := uiFixture(t, newUIFake(), realAssets(t))
	for _, path := range []string{"/", "/ui.js", "/ui.css", "/state", "/events", "/stop", "/abandon", "/run"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			req, _ := http.NewRequest(m, ts.URL+path, strings.NewReader("x"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, path, resp.StatusCode)
			}
		}
	}
	if resp, err := http.Get(ts.URL + "/nothing"); err != nil || resp.StatusCode != 404 {
		t.Fatalf("unknown path: %v %v", resp, err)
	}
}

func TestUIRefusesAForeignHostName(t *testing.T) {
	ts, _ := uiFixture(t, newUIFake(), realAssets(t))
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/state", nil)
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign host = %d, want 403", resp.StatusCode)
	}
}

func TestUIListensOnLoopbackOnly(t *testing.T) {
	ln, err := uiListen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	a := ln.Addr().(*net.TCPAddr)
	if !a.IP.Equal(net.IPv4(127, 0, 0, 1)) || a.Port == 0 {
		t.Fatalf("listening on %v", a)
	}
}

func TestUIStaysUpWhenTheEngineIsDown(t *testing.T) {
	fake := newUIFake()
	fake.down = errors.New("no engine")
	ts, _ := uiFixture(t, fake, realAssets(t))
	st := getState(t, ts)
	if !strings.Contains(st.Error, "no engine") || len(st.Runs) != 0 {
		t.Fatalf("state = %+v", st)
	}
	resp, err := http.Get(ts.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("page: %v %v", resp, err)
	}
	_ = resp.Body.Close()
}

func TestUIPageHasNoControls(t *testing.T) {
	assets := realAssets(t)
	page, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(assets, "ui.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<form", "<button", "<select", "<input", "<textarea", "method=", "onclick"} {
		if strings.Contains(strings.ToLower(string(page)), bad) {
			t.Errorf("the page holds the control %q", bad)
		}
	}
	for _, bad := range []string{"innerHTML", "POST", "PUT", "DELETE", "fetch(", "XMLHttpRequest", "eval("} {
		if strings.Contains(string(script), bad) {
			t.Errorf("the script holds %q", bad)
		}
	}
	for _, want := range []string{"intake", "build", "test_review", "commit", "cleanup", "done"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("the script lacks the stage %q", want)
		}
	}
}

func TestUIServeStopsWhenTheContextEnds(t *testing.T) {
	ln, err := uiListen()
	if err != nil {
		t.Fatal(err)
	}
	s := newUIServer(
		uiDeps{Engine: newUIFake(), Assets: fstest.MapFS{"index.html": {Data: []byte("x")}}, Every: 10 * time.Millisecond},
		ln.Addr().String(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/events")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop with an open event stream")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestUIReplaysARunOnlyWhenItsStatusChanged(t *testing.T) {
	old := uiReplayEvery
	uiReplayEvery = time.Hour
	t.Cleanup(func() { uiReplayEvery = old })
	fake := newUIFake()
	ts, _ := uiFixture(t, fake, realAssets(t))
	getState(t, ts)
	time.Sleep(200 * time.Millisecond) // about 20 polls at 10 ms
	if a, b := fake.replays(uiRunA), fake.replays(uiRunB); a != 1 || b != 1 {
		t.Fatalf("replays after quiet polls: run A %d, run B %d, want 1 each", a, b)
	}
	fake.moveB("cleanup")
	deadline := time.Now().Add(3 * time.Second)
	for fake.replays(uiRunB) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if a, b := fake.replays(uiRunA), fake.replays(uiRunB); a != 1 || b != 2 {
		t.Fatalf("replays after run B moved: run A %d, run B %d, want 1 and 2", a, b)
	}
}

func TestUIReadsAHungEngineWithinItsLimit(t *testing.T) {
	old := uiReadLimit
	uiReadLimit = 100 * time.Millisecond
	t.Cleanup(func() { uiReadLimit = old })
	fake := newUIFake()
	fake.hang = true
	ts, _ := uiFixture(t, fake, realAssets(t))
	st := getState(t, ts)
	if !strings.Contains(st.Error, "did not answer") {
		t.Fatalf("error = %q", st.Error)
	}
}

func TestUIStartsTheEngineOnceAcrossManyPolls(t *testing.T) {
	home := shortHome(t)
	eng := quickEngine(home)
	var mu sync.Mutex
	var fakeSrv *fakeServer
	starts := 0
	eng.Start = func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		starts++
		fakeSrv = startFake(
			t,
			home,
			func(_ *fakeServer, req engine.Request, send func(engine.Reply)) { emptyReply(req, send) },
		)
		return nil
	}
	h := newHarness(t)
	h.engine = eng
	oldEvery := uiEvery
	uiEvery = 10 * time.Millisecond
	t.Cleanup(func() { uiEvery = oldEvery })
	var st uiState
	withUIHooks(t, func(addr string) {
		mu.Lock()
		fakeSrv.Close() // the engine exits: no run is live and its last connection closed
		mu.Unlock()
		time.Sleep(300 * time.Millisecond) // about 30 polls
		resp, err := http.Get("http://" + addr + "/state")
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_ = json.NewDecoder(resp.Body).Decode(&st)
	})
	if code, e := h.envelope("ui"); code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 || st.Error != "the engine is not running" {
		t.Fatalf("engine started %d times, error %q; want 1 and the not-running error", starts, st.Error)
	}
}

// emptyReply answers a status request with no runs.
func emptyReply(_ engine.Request, send func(engine.Reply)) {
	b, _ := json.Marshal(StatusData{Runs: []RunRow{}, Queued: []QueuedRow{}, Blocked: []BlockedRow{}})
	send(engine.Reply{Status: engine.StatusOK, Data: b, Final: true})
}
