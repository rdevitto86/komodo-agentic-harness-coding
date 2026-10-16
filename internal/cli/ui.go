package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// UIData is the data of reply.ui.
type UIData struct {
	Address string `json:"address"` // host and port the dashboard listened on
}

// uiContext returns the context whose end stops the dashboard: it ends on Ctrl-C or a terminate signal.
var uiContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// uiStarted is called with the address once the dashboard listens; tests set it, nothing else does.
var uiStarted func(addr string)

// uiEvery is how often the dashboard asks the engine for the current state.
var uiEvery = 2 * time.Second

// uiReplayEvery is the longest a run's recorded events are kept before they are read again when its status is unchanged.
var uiReplayEvery = 10 * time.Second

// uiReadLimit is how long one read of the engine may take before the page shows the engine as not answering.
var uiReadLimit = 1500 * time.Millisecond

// uiPing is how often an idle event stream sends a comment line so the browser keeps it open.
const uiPing = 15 * time.Second

// uiListen opens a free port on the loopback address only.
func uiListen() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

// uiDeps are the parts the dashboard server reads.
type uiDeps struct {
	Engine Engine        // the control socket client; the dashboard only sends status and replay
	Root   string        // the repository root whose runs show; empty shows every repository
	Assets fs.FS         // the page files: index.html and ui.js
	Every  time.Duration // pause between engine reads; uiEvery when zero
}

// uiServer serves the read-only dashboard and keeps one shared copy of the latest engine state.
type uiServer struct {
	deps  uiDeps
	hosts map[string]bool // the Host header values a request may carry

	evCache map[string]uiRunEvents // events by run id; only collect touches it

	mu     sync.Mutex
	cur    []byte        // the latest state as JSON
	notify chan struct{} // closed and replaced when cur changes
	first  chan struct{} // closed after the first read of the engine
	once   sync.Once
}

// newUIServer returns a server that answers only requests addressed to addr or to localhost on its port.
func newUIServer(d uiDeps, addr string) *uiServer {
	if d.Every <= 0 {
		d.Every = uiEvery
	}
	_, port, _ := net.SplitHostPort(addr)
	d.Engine = uiReader(d.Engine)
	return &uiServer{
		deps: d, evCache: map[string]uiRunEvents{}, hosts: map[string]bool{addr: true, "localhost:" + port: true},
		notify: make(chan struct{}), first: make(chan struct{}),
	}
}

// uiReader returns the engine client the page polls with: a socket client that reports a missing engine instead of starting one.
func uiReader(eng Engine) Engine {
	se, ok := eng.(*SocketEngine)
	if !ok {
		return eng
	}
	c := *se
	c.Start = func(context.Context) error { return errUINoEngine }
	return &c
}

// handleUI serves the dashboard until Ctrl-C, starting the engine first when none runs.
func handleUI(c *Call) Result {
	ctx, stop := uiContext()
	defer stop()
	eng := c.cli.deps.Engine
	if err := eng.Ensure(ctx); err != nil {
		return internalErr("reach the engine: " + err.Error())
	}
	ln, err := uiListen()
	if err != nil {
		return internalErr("listen on the local address: " + err.Error())
	}
	assets, err := fs.Sub(komodo.Embedded, "embed/ui")
	if err != nil {
		_ = ln.Close()
		return internalErr("open the dashboard files: " + err.Error())
	}
	root := c.Repo.Root
	if c.Global.All {
		root = ""
	}
	addr := ln.Addr().String()
	s := newUIServer(uiDeps{Engine: eng, Root: root, Assets: assets}, addr)
	_, _ = fmt.Fprintf(c.cli.errOut, "Dashboard at http://%s (Ctrl-C to stop)\n", addr)
	if uiStarted != nil {
		uiStarted(addr)
	}
	if err := s.Serve(ctx, ln); err != nil {
		return internalErr("serve the dashboard: " + err.Error())
	}
	return Result{Outcome: OutcomeOK, Shape: "reply.ui", Facts: format.UIFacts{Address: addr}, Data: UIData{Address: addr}}
}

// Serve answers requests on ln and reads the engine until ctx ends, then closes every open stream and returns.
func (s *uiServer) Serve(ctx context.Context, ln net.Listener) error {
	srvCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{
		Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return srvCtx },
	}
	polled := make(chan struct{})
	go func() {
		defer fault.New(nil, nil).Recover(fault.Source{Component: "cli"}, nil)
		defer close(polled)
		s.Poll(srvCtx)
	}()
	errc := make(chan error, 1)
	go func() {
		defer fault.New(nil, nil).Recover(fault.Source{Component: "cli"}, nil)
		err := errors.New("the dashboard server stopped unexpectedly")
		defer func() { errc <- err }()
		err = srv.Serve(ln)
	}()
	select {
	case err := <-errc:
		cancel()
		<-polled
		return err
	case <-ctx.Done():
	}
	cancel()
	shut, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	err := srv.Shutdown(shut)
	<-polled
	if err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler returns the routes: the page, its script, the current state and the live event stream. Only GET and HEAD are served.
func (s *uiServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/state", s.state)
	mux.HandleFunc("/events", s.stream)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "the dashboard is read-only", http.StatusMethodNotAllowed)
			return
		}
		if !s.hosts[r.Host] {
			http.Error(w, "the dashboard answers only on its local address", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// page serves index.html at the root and ui.js beside it.
func (s *uiServer) page(w http.ResponseWriter, r *http.Request) {
	name, kind := "index.html", "text/html; charset=utf-8"
	switch r.URL.Path {
	case "/":
	case "/ui.js":
		name, kind = "ui.js", "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.deps.Assets, name)
	if err != nil {
		http.Error(w, "the dashboard file is missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// snapshot returns the latest state JSON and the channel that closes when it changes.
func (s *uiServer) snapshot() ([]byte, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.notify
}

// waitFirst blocks until the engine was read once, the request ends or 10 seconds pass.
func (s *uiServer) waitFirst(r *http.Request) bool {
	select {
	case <-s.first:
		return true
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
	return false
}

// state serves the current state as JSON.
func (s *uiServer) state(w http.ResponseWriter, r *http.Request) {
	if !s.waitFirst(r) {
		http.Error(w, "the engine has not answered yet", http.StatusServiceUnavailable)
		return
	}
	b, _ := s.snapshot()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// stream sends the state as a server-sent event now and again on every change, until the browser or the server ends it.
func (s *uiServer) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	if r.Method == http.MethodHead || !s.waitFirst(r) {
		return
	}
	ping := time.NewTicker(uiPing)
	defer ping.Stop()
	for {
		b, changed := s.snapshot()
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
			return
		}
		fl.Flush()
		for wait := true; wait; {
			select {
			case <-changed:
				wait = false
			case <-ping.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

// Poll reads the engine every interval and publishes the state until ctx ends.
func (s *uiServer) Poll(ctx context.Context) {
	t := time.NewTicker(s.deps.Every)
	defer t.Stop()
	for {
		s.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refresh reads the engine once and wakes every open stream when the state changed.
func (s *uiServer) refresh(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, uiReadLimit)
	b := mustJSONBytes(s.collect(rctx))
	cancel()
	s.mu.Lock()
	if !bytes.Equal(b, s.cur) {
		s.cur = b
		close(s.notify)
		s.notify = make(chan struct{})
	}
	s.mu.Unlock()
	s.once.Do(func() { close(s.first) })
}

// trimDetail keeps an error to its first line so one engine message stays one line on the page.
func trimDetail(err error) string {
	first, _, _ := strings.Cut(err.Error(), "\n")
	return first
}
