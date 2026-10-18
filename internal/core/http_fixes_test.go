package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamDropWithUnrewindableBodyIsNotResent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(sseHandler(2, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	req, err := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("prompt")))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("test body must not be rewindable")
	}
	_, err = h.c.Stream(context.Background(), epFor(t, srv.URL), req, col.hooks())
	var drop *dropError
	if !errors.As(err, &drop) || hits.Load() != 1 || col.restarts != 0 {
		t.Fatalf("err=%v hits=%d restarts=%d", err, hits.Load(), col.restarts)
	}
}

func TestTransportErrorsNeverCarryTheQuery(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(resetHandler(&hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	raw := srv.URL + "/v1/chat?key=SECRET"
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, raw))
	if err == nil || hits.Load() != MaxAttempts {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error text leaks the query: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) || strings.Contains(ue.URL, "SECRET") || !strings.Contains(ue.URL, "/v1/chat") {
		t.Errorf("wrapped url error not scrubbed: %+v", ue)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.logs) == 0 {
		t.Fatal("no retry lines logged")
	}
	for _, l := range h.logs {
		if strings.Contains(l, "SECRET") {
			t.Errorf("log leaks the query: %s", l)
		}
	}
}

func TestDoBodyStallFailsAsIdle(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("part"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	h := newHarness(t, Options{StreamIdle: 50 * time.Millisecond})
	start := time.Now()
	_, err := h.c.Do(context.Background(), epFor(t, srv.URL), get(t, srv.URL))
	if !errors.Is(err, ErrStreamIdle) || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
}

func TestStreamStallCountsAsDrop(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(stallHandler(&hits, release))
	defer srv.Close()
	defer close(release)
	h := newHarness(t, Options{StreamIdle: 50 * time.Millisecond})
	col := &collector{}
	_, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), col.hooks())
	if !errors.Is(err, ErrStreamIdle) || hits.Load() != 2 || col.restarts != 1 {
		t.Fatalf("err=%v hits=%d restarts=%d", err, hits.Load(), col.restarts)
	}
}

func TestSlowConsumerIsNotIdle(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(sseHandler(0, &hits))
	defer srv.Close()
	h := newHarness(t, Options{StreamIdle: 50 * time.Millisecond})
	hooks := StreamHooks{Event: func(SSEEvent) error { time.Sleep(120 * time.Millisecond); return nil }}
	if _, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), hooks); err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestFirstByteTimerFiringAfterResponseFailsTheCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h := newHarness(t, Options{})
	h.c.afterDo = func() { time.Sleep(150 * time.Millisecond) }
	ep := epFor(t, srv.URL)
	ep.FirstByte = 30 * time.Millisecond
	_, err := h.c.Do(context.Background(), ep, get(t, srv.URL))
	if !errors.Is(err, errFirstByte) {
		t.Fatalf("err=%v", err)
	}
}

func TestCleanEOFWithoutFinalEventIsADrop(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(finalHandler(99, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	hooks := col.hooks()
	hooks.Final = isDone
	_, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), hooks)
	if err == nil || hits.Load() != 2 || col.restarts != 1 {
		t.Fatalf("err=%v hits=%d restarts=%d", err, hits.Load(), col.restarts)
	}
}

func TestCleanEOFWithoutFinalEventRetriesOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(finalHandler(2, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	hooks := col.hooks()
	hooks.Final = isDone
	if _, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), hooks); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if hits.Load() != 2 || col.restarts != 1 || len(col.events) != 2 {
		t.Fatalf("hits=%d restarts=%d events=%+v", hits.Load(), col.restarts, col.events)
	}
}

func TestCleanEOFWithoutFinalHookStaysSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(finalHandler(99, &hits))
	defer srv.Close()
	h := newHarness(t, Options{})
	col := &collector{}
	if _, err := h.c.Stream(context.Background(), epFor(t, srv.URL), get(t, srv.URL), col.hooks()); err != nil ||
		hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestAllowlistEntryWithDefaultPortMatches(t *testing.T) {
	cases := []struct {
		entry, raw string
		want       bool
	}{
		{"api.anthropic.com:443", "https://api.anthropic.com/v1", true},
		{"API.Anthropic.com:443", "https://api.anthropic.com:443/v1", true},
		{"api.example.com:80", "http://api.example.com/x", true},
		{"api.anthropic.com:443", "http://api.anthropic.com/v1", false},
		{"api.anthropic.com:443", "https://api.anthropic.com:8443/v1", false},
		{"api.anthropic.com:8443", "https://api.anthropic.com:8443/v1", true},
		{"[::1]:443", "https://[::1]/v1", true},
		{"[::1]:8080", "http://[::1]:8080/v1", true},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := hostAllowed([]string{tc.entry}, u); got != tc.want {
			t.Errorf("entry %q url %q: got %v want %v", tc.entry, tc.raw, got, tc.want)
		}
	}
}

// resetHandler drops every connection without answering.
func resetHandler(hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}
}

// stallHandler sends one event, then goes silent until release closes.
func stallHandler(hits *atomic.Int32, release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

// finalHandler streams events and ends cleanly; requests from number completeOn on send the done event.
func finalHandler(completeOn int32, hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		if n >= completeOn {
			_, _ = w.Write([]byte("event: done\ndata: bye\n\n"))
		}
	}
}

// isDone matches the end-of-answer event.
func isDone(e SSEEvent) bool { return e.Event == "done" }
