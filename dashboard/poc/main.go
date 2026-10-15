// Command poc serves the dashboard proof of concept on mock data that moves: go run ./dashboard/poc
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/dashboard"
)

var (
	// blockReasons are the reasons the simulator blocks a group for.
	blockReasons = []string{"regression: tester failed 3 times", "merge conflict on the feature branch", "task list not met"}

	// maxFeed is how many events the simulator keeps.
	maxFeed = 30
)

// sim moves the mock view forward one step at a time.
type sim struct {
	v     dashboard.View // the view being written
	rng   *mrand.Rand    // seeded, so a demo plays the same way each time
	holds map[string]int // steps left until a held group resumes
	rest  map[string]int // steps a group has sat at done
}

// main starts the simulator and the server on the port in KOMODO_POC_PORT, a free one when unset.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run starts the simulator and the server; KOMODO_POC_USAGE=on shows tokens and cost.
func run() error {
	data, err := os.ReadFile("dashboard/testdata/groups.json")
	if err != nil {
		return fmt.Errorf("read mock view: %w", err)
	}

	var v dashboard.View
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("decode mock view: %w", err)
	}

	port := 0
	if p := os.Getenv("KOMODO_POC_PORT"); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return fmt.Errorf("read KOMODO_POC_PORT: %w", err)
		}
	}

	views := filepath.Join(os.TempDir(), "komodo-dashboard-view.json")
	s := newSim(v, time.Now())
	if err := write(views, s.v); err != nil {
		return err
	}
	go s.loop(views)

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("make token: %w", err)
	}

	usage := os.Getenv("KOMODO_POC_USAGE") == "on"
	return dashboard.Serve(dashboard.New(hex.EncodeToString(b), views, usage), port)
}

// newSim shifts the mock times so the newest event happened just now.
func newSim(v dashboard.View, now time.Time) *sim {
	var newest time.Time
	for _, e := range v.Events {
		if e.At.After(newest) {
			newest = e.At
		}
	}
	shift := now.Sub(newest)
	for i := range v.Events {
		v.Events[i].At = v.Events[i].At.Add(shift)
	}
	v.Run.Started = v.Run.Started.Add(shift)

	s := &sim{v: v, rng: mrand.New(mrand.NewPCG(7, 42)), holds: map[string]int{}, rest: map[string]int{}}
	for _, g := range v.Groups {
		if g.Hold != "" {
			s.holds[g.Group] = 14 + s.rng.IntN(4)
		}
	}
	return s
}

// loop steps the simulation every 2 seconds and writes the view.
func (s *sim) loop(views string) {
	for range time.Tick(2 * time.Second) {
		s.step(time.Now())
		_ = write(views, s.v)
	}
}

// step moves every group one step and drifts the plan windows.
func (s *sim) step(now time.Time) {
	for i := range s.v.Groups {
		s.moveGroup(&s.v.Groups[i], now)
	}
	for i := range s.v.Resources {
		l := &s.v.Resources[i]
		if l.Percent += s.rng.IntN(4); l.Percent > 100 {
			l.Percent = 20
		}
		if l.Percent == 90 {
			s.emit(now, "resource", "window-warn", "")
		}
	}
}

// moveGroup advances one group through intake, build, test_review, commit, cleanup and done, and restarts it after a rest.
func (s *sim) moveGroup(g *dashboard.Group, now time.Time) {
	if g.Stage != "done" {
		g.ElapsedMin++
	}

	if g.Hold != "" {
		if s.holds[g.Group]--; s.holds[g.Group] <= 0 {
			g.Hold, g.Block = "", ""
			if g.Stage == "build" {
				g.Sessions.Builder.Status = "running"
			}
			s.emit(now, "run-status", "resumed", g.Group)
		}
		return
	}

	switch g.Stage {
	case "intake":
		g.TasksTotal = 3 + s.rng.IntN(4)
		s.enter(g, "build", now)
		g.Sessions.Builder.Status = "running"
	case "build":
		s.build(g, now)
	case "test_review":
		s.testReview(g, now)
	case "commit":
		s.enter(g, "cleanup", now)
	case "cleanup":
		s.enter(g, "done", now)
		s.emit(now, "run-complete", "group-done", g.Group)
	case "done":
		if s.rest[g.Group]++; s.rest[g.Group] > 3 {
			s.rest[g.Group] = 0
			s.restart(g, now)
		}
	}
}

// build finishes a task, or occasionally blocks or pauses the group.
func (s *sim) build(g *dashboard.Group, now time.Time) {
	switch s.rng.IntN(30) {
	case 0:
		g.Hold, g.Block = "blocked", blockReasons[s.rng.IntN(len(blockReasons))]
		g.Sessions.Builder.Status = "fail"
		s.holds[g.Group] = 6
		s.emit(now, "run-failure", "group-blocked", g.Group)
		return
	case 1:
		g.Hold, g.Block = "paused", "plan window 92% on claude-max"
		s.holds[g.Group] = 5
		s.emit(now, "run-status", "paused", g.Group)
		return
	}

	use(&g.Sessions.Builder, s.rng.IntN(1500)+800)
	if g.TasksDone++; g.TasksDone >= g.TasksTotal {
		g.TasksDone = g.TasksTotal
		s.enter(g, "test_review", now)
		g.Sessions.Builder.Status = "pass"
		g.Sessions.Tester.Status, g.Sessions.Reviewer.Status = "running", "running"
	}
}

// testReview flips the tester and reviewer from running to pass or fail; a fail sends the group back to build.
func (s *sim) testReview(g *dashboard.Group, now time.Time) {
	for _, p := range []*dashboard.Session{&g.Sessions.Tester, &g.Sessions.Reviewer} {
		if p.Status != "running" {
			continue
		}
		use(p, s.rng.IntN(1200)+400)
		switch roll := s.rng.IntN(6); {
		case roll == 0:
			p.Status = "fail"
		case roll < 3:
			p.Status = "pass"
		}
	}

	t, r := g.Sessions.Tester.Status, g.Sessions.Reviewer.Status
	switch {
	case t == "fail" || r == "fail":
		g.TasksDone = max(g.TasksDone-1, 0)
		g.Sessions.Builder.Status = "running"
		g.Sessions.Tester.Status, g.Sessions.Reviewer.Status = "idle", "idle"
		s.enter(g, "build", now)
		s.emit(now, "run-failure", "tester-fail", g.Group)
	case t == "pass" && r == "pass":
		s.enter(g, "commit", now)
	}
}

// restart gives a finished group fresh work so the demo keeps moving.
func (s *sim) restart(g *dashboard.Group, now time.Time) {
	g.TasksDone, g.TasksTotal, g.ElapsedMin = 0, 0, 0
	g.Sessions.Builder = dashboard.Session{Status: "idle", Model: g.Sessions.Builder.Model}
	g.Sessions.Tester = dashboard.Session{Status: "idle", Model: g.Sessions.Tester.Model}
	g.Sessions.Reviewer = dashboard.Session{Status: "idle", Model: g.Sessions.Reviewer.Model}
	s.enter(g, "intake", now)
}

// enter moves a group to a stage and records the move in the feed.
func (s *sim) enter(g *dashboard.Group, stage string, now time.Time) {
	g.Stage = stage
	s.emit(now, "run-status", "stage-entered", g.Group)
}

// emit adds an event to the feed and keeps the newest maxFeed.
func (s *sim) emit(now time.Time, typ, subtype, group string) {
	s.v.Events = append(s.v.Events, dashboard.Event{Type: typ, Subtype: subtype, Group: group, At: now})
	if n := len(s.v.Events); n > maxFeed {
		s.v.Events = s.v.Events[n-maxFeed:]
	}
}

// use adds tokens and their cost to a session.
func use(p *dashboard.Session, tokens int) {
	p.Tokens += tokens
	p.USD += float64(tokens) * 0.000012
}

// write saves the view as JSON through a rename, so a reader never sees half a file.
func write(path string, v dashboard.View) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode view: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write view: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace view: %w", err)
	}
	return nil
}
