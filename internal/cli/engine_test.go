package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// fakeServer is a fake engine listening on a unix socket.
type fakeServer struct {
	ln      net.Listener
	mu      sync.Mutex
	reqs    []engine.Request
	conns   []net.Conn
	handler func(s *fakeServer, req engine.Request, send func(engine.Reply))
}

// startFake listens on home/state/engine.sock and serves each request line with handler.
func startFake(t *testing.T, home string, handler func(s *fakeServer, req engine.Request, send func(engine.Reply))) *fakeServer {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(home, "state", "engine.sock"))
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, handler: handler}
	t.Cleanup(s.Close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go s.serve(conn)
		}
	}()
	return s
}

// serve reads request lines from one connection and answers them.
func (s *fakeServer) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req engine.Request
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()
		s.handler(s, req, func(rep engine.Reply) {
			rep.ID = req.ID
			b, _ := json.Marshal(rep)
			conn.Write(append(b, '\n'))
		})
	}
}

// Close stops listening and drops every connection.
func (s *fakeServer) Close() {
	s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

// Requests returns the requests seen so far.
func (s *fakeServer) Requests() []engine.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]engine.Request(nil), s.reqs...)
}

// finalOK answers every request with one final ok frame.
func finalOK(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
	send(engine.Reply{Status: engine.StatusOK, Data: json.RawMessage(`{"ok":true}`), Final: true})
}

// quickEngine returns a client with short timers over home.
func quickEngine(home string) *SocketEngine {
	e := NewSocketEngine(home)
	e.Redial, e.StartWait, e.HungReply = 5*time.Millisecond, 500*time.Millisecond, 60*time.Millisecond
	return e
}

// drain reads every frame until the channel closes.
func drain(t *testing.T, ch <-chan engine.Reply) []engine.Reply {
	t.Helper()
	var out []engine.Reply
	timeout := time.After(5 * time.Second)
	for {
		select {
		case r, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, r)
		case <-timeout:
			t.Fatal("no final frame within 5 s")
		}
	}
}

// writeBeat writes engine.beat as the engine would.
func writeBeat(t *testing.T, home string, pid int, at time.Time) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pid": pid, "at": at.UTC().Format(time.RFC3339)})
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "engine.beat"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSendReadsUntilFinal(t *testing.T) {
	home := shortHome(t)
	startFake(t, home, func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		send(engine.Reply{Status: engine.StatusEvent, Event: &bus.Event{Type: bus.TypeRunStop, Run: "r", Data: &bus.RunStop{Target: "t"}}})
		send(engine.Reply{Status: engine.StatusOK, Data: json.RawMessage(`{"n":1}`)})
		send(engine.Reply{Status: engine.StatusOK, Final: true})
	})
	ch, err := quickEngine(home).Send(context.Background(), engine.Request{ID: "q1", Verb: engine.VerbReplay, Target: "tg-1.2"})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 3 || got[0].Status != engine.StatusEvent || got[0].Event == nil || !got[2].Final || got[0].ID != "q1" {
		t.Fatalf("frames = %+v", got)
	}
}

func TestEnsureStartsEngineOnDemandOnce(t *testing.T) {
	home := shortHome(t)
	var starts int
	var srv *fakeServer
	start := func(context.Context) error {
		starts++
		srv = startFake(t, home, finalOK)
		return nil
	}
	first := quickEngine(home)
	first.Start = start
	if err := first.Ensure(context.Background()); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	second := quickEngine(home)
	second.Start = start
	ch, err := second.Send(context.Background(), engine.Request{ID: "q", Verb: engine.VerbClean})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, ch); len(got) != 1 || !got[0].Final {
		t.Fatalf("frames = %+v", got)
	}
	if starts != 1 {
		t.Fatalf("engine started %d times, want 1", starts)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Fatalf("engine saw %d requests, want 1", n)
	}
}

func TestEnsureGivesUpNamingTheSocket(t *testing.T) {
	home := shortHome(t)
	e := quickEngine(home)
	e.StartWait = 80 * time.Millisecond
	e.Start = func(context.Context) error { return nil }
	err := e.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "state", "engine.sock")) {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleSocketFileStartsEngine(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "engine.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	started := false
	e := quickEngine(home)
	e.Start = func(context.Context) error {
		started = true
		os.Remove(filepath.Join(home, "state", "engine.sock"))
		startFake(t, home, finalOK)
		return nil
	}
	if err := e.Ensure(context.Background()); err != nil || !started {
		t.Fatalf("err = %v, started = %v", err, started)
	}
}

func TestHungEngineIsKilledRestartedAndAskedOnce(t *testing.T) {
	home := shortHome(t)
	hung := startFake(t, home, func(*fakeServer, engine.Request, func(engine.Reply)) {})
	writeBeat(t, home, 4242, time.Now().Add(-time.Minute))
	var killed []int
	var fresh *fakeServer
	e := quickEngine(home)
	e.Kill = func(pid int) error {
		killed = append(killed, pid)
		hung.Close()
		return nil
	}
	e.Start = func(context.Context) error {
		fresh = startFake(t, home, finalOK)
		return nil
	}
	ch, err := e.Send(context.Background(), engine.Request{ID: "h1", Verb: engine.VerbStatus})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 1 || got[0].Status != engine.StatusOK || !got[0].Final {
		t.Fatalf("frames = %+v", got)
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Fatalf("killed = %v", killed)
	}
	if n := len(hung.Requests()); n != 1 {
		t.Fatalf("hung engine saw %d requests", n)
	}
	if n := len(fresh.Requests()); n != 1 {
		t.Fatalf("fresh engine saw %d requests, want 1", n)
	}
}

func TestSlowEngineWithFreshBeatIsNotKilled(t *testing.T) {
	home := shortHome(t)
	startFake(t, home, func(_ *fakeServer, _ engine.Request, send func(engine.Reply)) {
		time.Sleep(200 * time.Millisecond)
		send(engine.Reply{Status: engine.StatusOK, Final: true})
	})
	writeBeat(t, home, 4242, time.Now())
	e := quickEngine(home)
	e.Kill = func(int) error { t.Error("a live engine was killed"); return nil }
	e.Start = func(context.Context) error { t.Error("a second engine was started"); return nil }
	ch, err := e.Send(context.Background(), engine.Request{ID: "s1", Verb: engine.VerbStatus})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, ch); len(got) != 1 || !got[0].Final {
		t.Fatalf("frames = %+v", got)
	}
}

func TestEngineStoppingStartsNewEngineAndResends(t *testing.T) {
	home := shortHome(t)
	stopping := startFake(t, home, func(s *fakeServer, _ engine.Request, send func(engine.Reply)) {
		s.ln.Close()
		send(engine.Reply{Status: engine.StatusRefused, Code: engine.CodeEngineStopping, Final: true})
	})
	var fresh *fakeServer
	e := quickEngine(home)
	e.Start = func(context.Context) error {
		fresh = startFake(t, home, finalOK)
		return nil
	}
	ch, err := e.Send(context.Background(), engine.Request{ID: "e1", Verb: engine.VerbRun})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 1 || got[0].Status != engine.StatusOK {
		t.Fatalf("frames = %+v", got)
	}
	if len(stopping.Requests()) != 1 || len(fresh.Requests()) != 1 {
		t.Fatalf("requests: old %d, new %d", len(stopping.Requests()), len(fresh.Requests()))
	}
}

func TestBrokenConnectionEndsWithErrorFrame(t *testing.T) {
	home := shortHome(t)
	startFake(t, home, func(s *fakeServer, _ engine.Request, _ func(engine.Reply)) {
		s.Close()
	})
	ch, err := quickEngine(home).Send(context.Background(), engine.Request{ID: "b1", Verb: engine.VerbStatus})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 1 || got[0].Status != engine.StatusError || !got[0].Final {
		t.Fatalf("frames = %+v", got)
	}
}

func TestDefaultSpawnIsTheHiddenVerb(t *testing.T) {
	e := NewSocketEngine("/h")
	if strings.Join(e.Args, " ") != "internal engine" || e.Redial != 50*time.Millisecond || e.StartWait != 5*time.Second ||
		e.HungReply != 10*time.Second || e.HungBeat != 30*time.Second {
		t.Fatalf("defaults = %+v", e)
	}
}

func TestSpawnStartsDetachedProcessThatServes(t *testing.T) {
	home := shortHome(t)
	e := quickEngine(home)
	e.StartWait = 10 * time.Second
	e.Exe, e.Args = os.Args[0], []string{"-test.run=^TestHelperEngine$"}
	t.Setenv("GO_WANT_HELPER_ENGINE", "1")
	t.Setenv("KOMODO_HOME", home)
	ch, err := e.Send(context.Background(), engine.Request{ID: "p1", Verb: engine.VerbClean})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 1 || !strings.Contains(string(got[0].Data), "spawned") {
		t.Fatalf("frames = %+v", got)
	}
}

// TestHelperEngine is not a test: it is the engine process the spawn test starts.
func TestHelperEngine(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_ENGINE") != "1" {
		return
	}
	state := filepath.Join(os.Getenv("KOMODO_HOME"), "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		os.Exit(2)
	}
	ln, err := net.Listen("unix", filepath.Join(state, "engine.sock"))
	if err != nil {
		os.Exit(2)
	}
	time.AfterFunc(20*time.Second, func() { os.Exit(3) })
	for {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(2)
		}
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			conn.Close()
			continue
		}
		var req engine.Request
		_ = json.Unmarshal(line, &req)
		b, _ := json.Marshal(engine.Reply{ID: req.ID, Status: engine.StatusOK, Data: json.RawMessage(`{"spawned":true}`), Final: true})
		conn.Write(append(b, '\n'))
		os.Exit(0)
	}
}
