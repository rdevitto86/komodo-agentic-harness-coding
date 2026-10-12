package core

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const plantedKey = "sk-ant-PLANTED-0123456789"

// harness is a Client wired with recorded sleeps and logs.
type harness struct {
	c      *Client
	mu     sync.Mutex
	sleeps []time.Duration
	logs   []string
}

// newHarness builds a Client whose sleeps return at once.
func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{}
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		h.mu.Lock()
		h.sleeps = append(h.sleeps, d)
		h.mu.Unlock()
		return nil
	}
	opts.Log = func(s string) {
		h.mu.Lock()
		h.logs = append(h.logs, s)
		h.mu.Unlock()
	}
	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	h.c = c
	return h
}

// epFor allows exactly the host of a test server URL.
func epFor(t *testing.T, raw string) Endpoint {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return Endpoint{Hosts: []string{u.Host}}
}

// get builds a GET request for raw.
func get(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// status429 answers a rate-limit response with an optional retry-after.
func status429(w http.ResponseWriter, retryAfter, body string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(body))
}

func TestRetry429ThenOKSucceedsOnAttempt2(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			status429(w, "", `{"error":{"type":"rate_limit_error"}}`)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	resp, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Attempts != 2 || string(resp.Body) != "ok" || len(h.sleeps) != 1 {
		t.Fatalf("attempts=%d body=%q sleeps=%v", resp.Attempts, resp.Body, h.sleeps)
	}
}

func TestRetryAfterReplacesJitter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			status429(w, "7", "{}")
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	if _, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL)); err != nil {
		t.Fatal(err)
	}
	if len(h.sleeps) != 1 || h.sleeps[0] != 7*time.Second {
		t.Fatalf("sleeps=%v want [7s]", h.sleeps)
	}
}

func TestSpendLimit429ReturnsLimitAfterOneAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		status429(w, "", `{"type":"error","error":{"type":"rate_limit_error","details":{"error_code":"enforced_spend_limit_reached"}}}`)
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitSpend {
		t.Fatalf("err=%v want spend LimitError", err)
	}
	if hits.Load() != 1 || len(h.sleeps) != 0 {
		t.Fatalf("hits=%d sleeps=%v", hits.Load(), h.sleeps)
	}
}

func TestQuotaErrorInBodyReturnsLimit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		status429(w, "5", `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`)
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitQuota || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func Test529IsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(529)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	resp, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	if err != nil || resp.Attempts != 2 {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
}

func TestRetrySleepsWithin30sAndVary(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1)%2 == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	for i := 0; i < 100; i++ {
		if _, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(h.sleeps) != 100 {
		t.Fatalf("sleeps=%d want 100", len(h.sleeps))
	}
	seen := map[time.Duration]bool{}
	for _, d := range h.sleeps {
		if d < 0 || d > 30*time.Second {
			t.Fatalf("sleep %v outside [0,30s]", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("all 100 sleeps identical")
	}
}

func TestBackoffCeilingAndCap(t *testing.T) {
	max := func(n int64) int64 { return n - 1 }
	cases := map[int]time.Duration{0: 1*time.Second - 1, 1: 2*time.Second - 1, 2: 4*time.Second - 1, 20: 30*time.Second - 1}
	for attempt, want := range cases {
		if got := backoff(attempt, max); got != want {
			t.Errorf("attempt %d: got %v want %v", attempt, got, want)
		}
	}
}

func TestRetryAfter3600ReturnsLimitAfterOneAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		status429(w, "3600", "{}")
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	var le *LimitError
	if !errors.As(err, &le) || le.Kind != LimitRetryAfter || le.RetryAfter != time.Hour {
		t.Fatalf("err=%v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits=%d want 1", hits.Load())
	}
}

func TestGivesUpAfterFourAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 502 || se.Attempts != 4 {
		t.Fatalf("err=%v", err)
	}
	if hits.Load() != 4 || len(h.sleeps) != 3 {
		t.Fatalf("hits=%d sleeps=%d", hits.Load(), len(h.sleeps))
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 400 || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestConnectionResetIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	resp, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	if err != nil || resp.Attempts != 2 {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
}

// sseHandler streams events, aborting after cutAfter events on the first cuts requests.
func sseHandler(cuts int32, hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte("event: a\ndata: one\n\n"))
		fl.Flush()
		if n <= cuts {
			_, _ = w.Write([]byte("data: par"))
			fl.Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write([]byte(": keepalive\n\ndata: two\ndata: lines\nid: 9\n\n"))
	}
}

// collector gathers stream output and drops it on restart.
type collector struct {
	events   []SSEEvent
	restarts int
	activity int
}

// hooks wires the collector into StreamHooks.
func (c *collector) hooks() StreamHooks {
	return StreamHooks{
		Activity: func() { c.activity++ },
		Event:    func(e SSEEvent) error { c.events = append(c.events, e); return nil },
		Restart:  func() { c.restarts++; c.events = nil },
	}
}

func TestStreamParsesEventsAndReportsActivity(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(sseHandler(0, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	if _, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), col.hooks()); err != nil {
		t.Fatal(err)
	}
	want := []SSEEvent{{Event: "a", Data: "one"}, {Data: "two\nlines", ID: "9"}}
	if len(col.events) != 2 || col.events[0] != want[0] || col.events[1] != want[1] {
		t.Fatalf("events=%+v", col.events)
	}
	if col.activity == 0 {
		t.Fatal("activity hook never fired")
	}
}

func TestStreamCutOnceSucceedsOnRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(sseHandler(1, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	if _, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), col.hooks()); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if col.restarts != 1 || len(col.events) != 2 || hits.Load() != 2 {
		t.Fatalf("restarts=%d events=%+v hits=%d", col.restarts, col.events, hits.Load())
	}
}

func TestStreamCutTwiceFails(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(sseHandler(2, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	_, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), col.hooks())
	if err == nil || hits.Load() != 2 || col.restarts != 1 {
		t.Fatalf("err=%v hits=%d restarts=%d", err, hits.Load(), col.restarts)
	}
}

func TestPlantedKeyNeverReachesLogsOrErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream saw " + r.Header.Get("X-Api-Key") + " and " + plantedKey))
	}))
	defer srv.Close()
	envValue := "env-sourced-VALUE-42"
	h := newHarness(t, Options{Secrets: []string{envValue}})
	var seen []string

	req := get(t, srv.URL+"?token="+envValue)
	req.Header.Set("X-Api-Key", plantedKey)
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), req)
	seen = append(seen, err.Error())

	closed := httptest.NewServer(http.NotFoundHandler())
	dead := closed.URL
	closed.Close()
	req = get(t, dead+"?key="+plantedKey+"&t="+envValue)
	req.Header.Set("Authorization", "Bearer "+plantedKey)
	_, err = h.c.Do(context.Background(), epFor(t, dead), req)
	seen = append(seen, err.Error())

	seen = append(seen, h.logs...)
	if len(h.logs) == 0 {
		t.Fatal("expected retry log lines")
	}
	for _, s := range seen {
		if strings.Contains(s, plantedKey) || strings.Contains(s, envValue) {
			t.Fatalf("secret leaked: %q", s)
		}
	}
	var se *StatusError
	_, err = h.c.Do(context.Background(), epFor(t, srv.URL), req2(t, srv.URL))
	if !errors.As(err, &se) || strings.Contains(string(se.Body), plantedKey) {
		t.Fatalf("status body leaked: %v", err)
	}
}

// req2 builds a request carrying the planted key as a header.
func req2(t *testing.T, raw string) *http.Request {
	r := get(t, raw)
	r.Header.Set("X-Api-Key", plantedKey)
	return r
}

// writeCA writes the server certificate of srv as a PEM bundle.
func writeCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTestCAFailsWithoutBundleAndPassesWith(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secure"))
	}))
	defer srv.Close()
	ep := epFor(t, srv.URL)

	h := newHarness(t, Options{})
	if _, err := h.c.Do(context.Background(), ep, get(t, srv.URL)); err == nil {
		t.Fatal("expected certificate failure without bundle")
	}
	h = newHarness(t, Options{CABundle: writeCA(t, srv)})
	resp, err := h.c.Do(context.Background(), ep, get(t, srv.URL))
	if err != nil || string(resp.Body) != "secure" {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
}

func TestBadCABundleIsAnError(t *testing.T) {
	if _, err := NewClient(Options{CABundle: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Error("missing file accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(Options{CABundle: junk}); err == nil {
		t.Error("junk file accepted")
	}
}

func TestPlainHTTPToNonLoopbackIsRefused(t *testing.T) {
	h := newHarness(t, Options{})
	ep := Endpoint{Hosts: []string{"models.example.com"}}
	_, err := h.c.Do(context.Background(), ep, get(t, "http://models.example.com/v1"))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err=%v want ErrRefused", err)
	}
	u, _ := url.Parse("http://models.example.com/v1")
	warn, err := h.c.vet(Endpoint{Hosts: ep.Hosts, SelfHosted: true}, u, http.MethodGet)
	if err != nil || !strings.Contains(warn, "⚠️") {
		t.Fatalf("self_hosted warn=%q err=%v", warn, err)
	}
	u, _ = url.Parse("http://127.0.0.1:9/")
	warn, err = h.c.vet(Endpoint{Hosts: []string{"127.0.0.1:9"}}, u, http.MethodGet)
	if err != nil || warn != "" {
		t.Fatalf("loopback warn=%q err=%v", warn, err)
	}
}

func TestHostAndMethodAllowlists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	h := newHarness(t, Options{})
	if _, err := h.c.Do(context.Background(), Endpoint{Hosts: []string{"other.example.com"}}, get(t, srv.URL)); !errors.Is(err, ErrRefused) {
		t.Errorf("host outside allowlist: %v", err)
	}
	if _, err := h.c.Do(context.Background(), Endpoint{}, get(t, srv.URL)); !errors.Is(err, ErrRefused) {
		t.Errorf("empty allowlist: %v", err)
	}
	ep := epFor(t, srv.URL)
	ep.Methods = []string{http.MethodGet}
	post, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("x"))
	if _, err := h.c.Do(context.Background(), ep, post); !errors.Is(err, ErrRefused) {
		t.Errorf("POST on GET-only endpoint: %v", err)
	}
	if _, err := h.c.Do(context.Background(), ep, get(t, srv.URL)); err != nil {
		t.Errorf("GET: %v", err)
	}
}

func TestRedirects(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/local":
			http.Redirect(w, r, "/done", http.StatusFound)
		default:
			_, _ = w.Write([]byte("done"))
		}
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	ep := epFor(t, srv.URL)
	if _, err := h.c.Do(context.Background(), ep, get(t, srv.URL+"/away")); !errors.Is(err, ErrRefused) || otherHits.Load() != 0 {
		t.Fatalf("cross-host redirect: err=%v hits=%d", err, otherHits.Load())
	}
	resp, err := h.c.Do(context.Background(), ep, get(t, srv.URL+"/local"))
	if err != nil || string(resp.Body) != "done" {
		t.Fatalf("same-host redirect: err=%v", err)
	}
}

func TestBodyCapRefusesLargeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()
	h := newHarness(t, Options{MaxBody: 10})
	if _, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL)); err == nil {
		t.Fatal("expected body cap error")
	}
	if MaxBody != 8*1024*1024 {
		t.Fatalf("default cap %d", MaxBody)
	}
}

func TestNoCookiesKept(t *testing.T) {
	var sawCookie atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			sawCookie.Store(true)
		}
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	for i := 0; i < 2; i++ {
		if _, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL)); err != nil {
			t.Fatal(err)
		}
	}
	if sawCookie.Load() {
		t.Fatal("client replayed a cookie")
	}
}

func TestLoopbackIsNeverProxied(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1) }))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("direct")) }))
	defer srv.Close()
	h := newHarness(t, Options{Proxy: func(*http.Request) (*url.URL, error) { return pu, nil }})
	resp, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	if err != nil || string(resp.Body) != "direct" || proxyHits.Load() != 0 {
		t.Fatalf("err=%v proxyHits=%d", err, proxyHits.Load())
	}
	got, _ := h.c.proxyFor(get(t, "https://api.example.com/"))
	if got == nil || got.Host != pu.Host {
		t.Fatalf("remote host not proxied: %v", got)
	}
}

func TestHeadroomIsReadAndWarns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Anthropic-Ratelimit-Requests-Limit", "100")
		w.Header().Set("Anthropic-Ratelimit-Requests-Remaining", "5")
		w.Header().Set("Anthropic-Ratelimit-Requests-Reset", "2026-10-11T12:00:00Z")
		w.Header().Set("Anthropic-Ratelimit-Tokens-Limit", "1000")
		w.Header().Set("Anthropic-Ratelimit-Tokens-Remaining", "900")
	}))
	defer srv.Close()
	var warned []Headroom
	h := newHarness(t, Options{WarnQuotaPct: 80, OnWarn: func(hr Headroom) { warned = append(warned, hr) }})
	resp, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Headroom) != 2 {
		t.Fatalf("headroom=%+v", resp.Headroom)
	}
	if len(warned) != 1 || warned[0].Name != "requests" || warned[0].Remaining != 5 || warned[0].Reset.IsZero() {
		t.Fatalf("warned=%+v", warned)
	}
}

func TestFirstByteTimeoutFailsTheCall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	h := newHarness(t, Options{})
	ep := epFor(t, srv.URL)
	ep.FirstByte = 50 * time.Millisecond
	_, err := h.c.Do(context.Background(), ep, get(t, srv.URL))
	if err == nil || !strings.Contains(err.Error(), "first byte") {
		t.Fatalf("err=%v", err)
	}
}

func TestTimeoutDefaults(t *testing.T) {
	remote, _ := url.Parse("https://api.example.com/")
	local, _ := url.Parse("http://localhost:11434/")
	hosted := Endpoint{}
	self := Endpoint{SelfHosted: true}
	if hosted.StreamIdle(remote) != 90*time.Second || hosted.StreamIdle(local) != 300*time.Second || self.StreamIdle(remote) != 300*time.Second {
		t.Error("stream idle defaults wrong")
	}
	if hosted.FirstByteTimeout(remote) != 60*time.Second || hosted.FirstByteTimeout(local) != 300*time.Second {
		t.Error("first byte defaults wrong")
	}
	if (Endpoint{FirstByte: 5 * time.Second}).FirstByteTimeout(remote) != 5*time.Second {
		t.Error("per-endpoint override ignored")
	}
}
