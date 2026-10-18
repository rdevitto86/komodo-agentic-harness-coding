package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// errBody stands in for the data of an error event, which another package owns.
type errBody struct {
	Severity string `json:"severity"`
}

// t0 is the fixed start time of the hand-built events.
var t0 = time.Date(2026, 10, 14, 15, 20, 0, 0, time.UTC)

// EventKey binds the stand-in to error / fault.error.
func (errBody) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "fault.error"} }

// fixture is a temporary repository and home folder with a bus, a fault handler and a Service.
type fixture struct {
	t       *testing.T
	root    string // the repository root
	common  string // the repository's git common directory
	repoDir string // the repository's metrics folder
	events  string // the repository's events folder, which the bus fills
	homeDir string // the machine metrics folder passed to New
	bus     bus.Bus
	svc     *Service
}

// newFixture makes a git repository and a home folder in temporary folders and a Service over them.
func newFixture(t *testing.T, buffer int, mask func(string) string) *fixture {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("KOMODO_HOME", home)
	common := filepath.Join(root, ".git")
	b := bus.New(bus.Options{BufferEvents: buffer, EventsDir: filepath.Join(home, "events")})
	t.Cleanup(func() { _ = b.Close() })
	hd := filepath.Join(home, "metrics")
	svc, err := New(b, hd, Options{RetentionDays: 30}, mask)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t:       t,
		root:    root,
		common:  common,
		repoDir: RepoDir(common),
		events:  bus.EventsDir(common),
		homeDir: hd,
		bus:     b,
		svc:     svc,
	}
}

// publish puts events on the bus and fails the test on a refusal.
func (f *fixture) publish(evs ...bus.Event) {
	f.t.Helper()
	for _, e := range evs {
		if err := f.bus.Publish(e); err != nil {
			f.t.Fatalf("publish %s/%s: %v", e.Type, e.Subtype, err)
		}
	}
}

// start returns a function that stops the Service after it has drained the queue.
func (f *fixture) start() (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.svc.Run(ctx) }()
	return func() {
		f.t.Helper()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				f.t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			f.t.Fatal("Run did not return")
		}
	}
}

// runStart builds the run-start command of a run in the fixture repository.
func (f *fixture) runStart(run bus.ID, groups ...string) bus.Event {
	return bus.Event{
		Type: bus.TypeRunStart,
		Run:  run,
		Data: &bus.RunStart{Target: "ep-1", Groups: groups, Source: "cli", Repo: "r", Root: f.root, CommonDir: f.common},
	}
}

// TestMain binds the stand-in error event once, as the program binds the real ones before its first publish.
func TestMain(m *testing.M) {
	bus.Register(errBody{}.EventKey(), func() bus.Payload { return &errBody{} })
	os.Exit(m.Run())
}

// ev builds an event stamped sec seconds after t0.
func ev(sec int, typ bus.Type, sub bus.Subtype, run, group bus.ID, data bus.Payload) bus.Event {
	return bus.Event{
		Type:    typ,
		Subtype: sub,
		Run:     run,
		Group:   group,
		Time:    t0.Add(time.Duration(sec) * time.Second),
		Data:    data,
	}
}

// facts builds the group facts of a session event.
func facts(stage string, session bus.ID) bus.GroupFacts {
	return bus.GroupFacts{Stage: stage, Session: session}
}

// lines returns the non-empty lines of a file, or none when it is missing.
func lines(t *testing.T, path string) []string {
	t.Helper()
	fh, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if sc.Text() != "" {
			out = append(out, sc.Text())
		}
	}
	return out
}

// jsonLine marshals an event as one log line.
func jsonLine(t *testing.T, e bus.Event) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeLog writes raw content to a run's event log in the events folder dir.
func writeLog(t *testing.T, dir string, run bus.ID, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, bus.LogName(run))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// joinLines joins lines with newlines and a closing newline.
func joinLines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }
