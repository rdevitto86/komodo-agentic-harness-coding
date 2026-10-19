package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// eng is an Engine serving on a short socket path over the rig's fakes.
type eng struct {
	*rig
	e      *Engine
	sock   string
	served chan error
	stop   context.CancelFunc
	over   bool     // Serve has returned and its result was taken
	hold   net.Conn // keeps the exit rule from firing before the test asks for it
}

// newEng starts an engine; the socket sits in a short folder because unix socket paths are short.
func newEng(t *testing.T, defs [][]string, opts ...rigOpt) *eng {
	t.Helper()
	dir, err := os.MkdirTemp("", "ke")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	r := newRig(t, defs, append([]rigOpt{func(r *rig) { r.noRun = true }}, opts...)...)
	x := &eng{rig: r, sock: filepath.Join(dir, SocketName), served: make(chan error, 1)}
	x.e = NewEngine(r.d, x.sock)
	r.m = x.e.Manager()
	return x
}

// serve runs Serve in the background and waits for the socket to answer.
func (x *eng) serve() {
	x.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	x.stop = cancel
	go func() { x.served <- x.e.Serve(ctx) }()
	x.t.Cleanup(x.drain)
	until(x.t, "the socket to answer", func() bool {
		c, err := net.Dial("unix", x.sock)
		if err != nil {
			return false
		}
		x.hold = c
		return true
	})
}

// exited waits for Serve to return and gives its error.
func (x *eng) exited() error {
	x.t.Helper()
	x.release()
	select {
	case err := <-x.served:
		x.over = true
		return err
	case <-time.After(5 * time.Second):
		x.t.Fatal("the engine did not exit")
	}
	return nil
}

// drain stops Serve and waits for it, so no goroutine of the engine writes after the test folder goes.
func (x *eng) drain() {
	x.release()
	x.stop()
	if x.over {
		return
	}

	select {
	case <-x.served:
	case <-time.After(5 * time.Second):
		x.t.Error("the engine did not exit on cleanup")
	}
}

// release closes the connection that holds the engine up.
func (x *eng) release() {
	if x.hold != nil {
		_ = x.hold.Close()
		x.hold = nil
	}
}

// ask sends one request on a new connection and returns every reply up to the final one.
func (x *eng) ask(req Request) []Reply {
	x.t.Helper()
	c, err := Dial(x.sock)
	if err != nil {
		x.t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	return roundTrip(x.t, c, req)
}

// runReq builds a run request carrying a run-start event.
func (x *eng) runReq(id, runID string, labels ...string) Request {
	rs := &bus.RunStart{
		Target:    "ep-1",
		Groups:    labels,
		Source:    "cli",
		Repo:      "github.com/acme/shop",
		Root:      x.root,
		CommonDir: filepath.Join(x.root, ".git"),
		Request:   bus.ID(id),
	}
	return Request{
		ID:     id,
		Verb:   VerbRun,
		Target: "ep-1",
		Root:   x.root,
		Event:  &Event{Type: bus.TypeRunStart, Run: bus.ID(runID), Data: rs},
	}
}

// fakeReader serves a canned replay.
type fakeReader struct{ log bus.ReplayLog }

// Replay returns the canned log.
func (f fakeReader) Replay(context.Context, bus.ID, bus.ID) (bus.ReplayLog, error) { return f.log, nil }

// Explain returns nothing.
func (fakeReader) Explain(context.Context, bus.ID, int) ([]bus.Event, error) { return nil, nil }

// fakeJanitor records the clean request.
type fakeJanitor struct {
	mu   sync.Mutex
	dry  bool
	open map[string]bool
}

// Clean records its arguments.
func (j *fakeJanitor) Clean(_ context.Context, dry bool, _ string, open map[string]bool) ([]CleanItem, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.dry, j.open = dry, open
	return []CleanItem{{Kind: "worktree", Name: "old"}}, nil
}

func TestServeRunsAGroupThroughTheSocketAndExitsWhenIdle(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	reps := x.ask(x.runReq("q1", "run-1", "tg-1"))
	last := reps[len(reps)-1]
	var d struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(last.Data, &d)
	if last.Status != StatusOK || d.RunID != "run-1" {
		t.Fatalf("reply %+v", last)
	}
	if err := x.exited(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(x.sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file stays after the exit: %v", err)
	}
	if got := x.store.get("g-tg-1").Stage; got != StageDone {
		t.Fatalf("stage %q: the engine exited before the run finished", got)
	}
	if x.caches.closes.Load() == 0 {
		t.Fatal("the cache was not closed")
	}
}

func TestBadFrameKeepsTheConnectionOpen(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	c, err := Dial(x.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.SendRaw([]byte(`{"id":"b1","verb":`)); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Recv()
	if err != nil || rep.Code != CodeBadFrame || !rep.Final {
		t.Fatalf("reply %+v err %v", rep, err)
	}
	reps := roundTrip(t, c, Request{ID: "s1", Verb: VerbStatus, Root: x.root})
	if reps[len(reps)-1].Status != StatusOK {
		t.Fatalf("status after a bad frame: %+v", reps)
	}
}

func TestSecondEngineExitsAndALeftoverSocketFileIsReplaced(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	second := NewEngine(x.d, x.sock)
	if err := second.Serve(context.Background()); !errors.Is(err, ErrEngineRunning) {
		t.Fatalf("second engine returned %v", err)
	}
	c, err := Dial(x.sock)
	if err != nil {
		t.Fatalf("the first engine stopped answering: %v", err)
	}
	_ = c.Close()

	dir, _ := os.MkdirTemp("", "ke")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, SocketName)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatal("no leftover socket file to test with")
	}
	y := &eng{rig: x.rig, sock: sock, served: make(chan error, 1)}
	y.e = NewEngine(x.d, sock)
	y.serve()
	if info, err := os.Stat(sock); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v err %v", info, err)
	}
}

func TestDialWithNoEngineReturnsErrNoEngine(t *testing.T) {
	dir, _ := os.MkdirTemp("", "ke")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if _, err := Dial(filepath.Join(dir, "x.sock")); !errors.Is(err, ErrNoEngine) {
		t.Fatalf("got %v", err)
	}
}

func TestEngineStaysWhileAConnectionIsOpenAndAnExitingEngineAnswersEngineStopping(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	c, err := Dial(x.sock)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, Request{ID: "s1", Verb: VerbStatus, Root: x.root})
	select {
	case err := <-x.served:
		t.Fatalf("the engine exited with a connection open: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	x.e.mu.Lock()
	x.e.closing = true
	x.e.mu.Unlock()
	late, err := Dial(x.sock)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := late.Recv()
	if err != nil || rep.Status != StatusRefused || rep.Code != CodeEngineStopping {
		t.Fatalf("late reply %+v err %v", rep, err)
	}
	_ = late.Close()
	_ = c.Close()
}

func TestOverdueEntryStartsAtOnceFutureEntryKeepsTheEngineUpAndStopRemovesIt(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}, {"tg-2"}})
	now := time.Now()
	mk := func(run, label string, at time.Time) state.ScheduleEntry {
		return state.ScheduleEntry{Target: label, Root: x.root, RunID: run, At: at, QueuedAt: now,
			Start: bus.RunStart{
				Target:    label,
				Groups:    []string{label},
				Source:    "cli",
				Repo:      "github.com/acme/shop",
				Root:      x.root,
				CommonDir: filepath.Join(x.root, ".git"),
				Schedule:  at,
			}}
	}
	body := state.ScheduleBody{
		Entries: []state.ScheduleEntry{
			mk("run-overdue", "tg-1", now.Add(-time.Minute)),
			mk("run-future", "tg-2", now.Add(time.Hour)),
		},
	}
	if _, err := state.SaveMachine(x.d.Machine, state.KindSchedule, body, 0); err != nil {
		t.Fatal(err)
	}
	x.serve()
	x.waitStage("tg-1", StageDone)
	until(t, "the overdue entry removed", func() bool {
		rec, _ := state.LoadMachine[state.ScheduleBody](x.d.Machine, state.KindSchedule)
		return len(rec.Body.Entries) == 1 && rec.Body.Entries[0].RunID == "run-future"
	})
	for _, e := range x.ev.list() {
		if e.Type == bus.TypeRunStart && e.Run == "run-overdue" {
			goto started
		}
	}
	t.Fatal("no run-start carried the entry's run id")
started:
	select {
	case err := <-x.served:
		t.Fatalf("the engine exited with an entry queued: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if x.tasks.startedCount("g-tg-2") != 0 {
		t.Fatal("the future entry started early")
	}
	reps := x.ask(Request{ID: "st", Verb: VerbStatus, Root: x.root})
	var sd StatusData
	_ = json.Unmarshal(reps[len(reps)-1].Data, &sd)
	if len(sd.Queued) != 1 || sd.Queued[0].RunID != "run-future" {
		t.Fatalf("status queue %+v", sd.Queued)
	}
	reps = x.ask(Request{ID: "sp", Verb: VerbStop, Target: "tg-2"})
	if reps[len(reps)-1].Status != StatusOK {
		t.Fatalf("stop reply %+v", reps[len(reps)-1])
	}
	if err := x.exited(); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleRequestWritesTheEntryBeforeItRepliesAndTheEntryFiresOnce(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	req := x.runReq("q1", "", "tg-1")
	at := time.Now().Add(400 * time.Millisecond).UTC().Truncate(time.Millisecond)
	req.Flags.Schedule = at
	reps := x.ask(req)
	var d struct {
		RunID  string `json:"run_id"`
		Queued struct {
			At time.Time `json:"at"`
		} `json:"queued"`
	}
	_ = json.Unmarshal(reps[len(reps)-1].Data, &d)
	if d.RunID == "" {
		t.Fatalf("no run id in %+v", reps[len(reps)-1])
	}
	rec, err := state.LoadMachine[state.ScheduleBody](x.d.Machine, state.KindSchedule)
	if err != nil || len(rec.Body.Entries) == 0 && x.store.get("g-tg-1").Stage == NoStage {
		t.Fatalf("entry not written before the reply: %v %+v", err, rec.Body)
	}
	x.waitStage("tg-1", StageDone)
	if err := x.exited(); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range x.ev.list() {
		if e.Type == bus.TypeRunStart && string(e.Run) == d.RunID {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("run-start published %d times for run id %s", found, d.RunID)
	}
}

func TestQueuedEntryWhoseGroupAlreadyHasARecordContinuesWithoutADuplicate(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle)
	x.savedGroup("tg-1", StageBuild, nil)
	now := time.Now()
	e := state.ScheduleEntry{Target: "tg-1", Root: x.root, RunID: "run-q", At: now.Add(-time.Second), QueuedAt: now,
		Start: bus.RunStart{
			Target:    "tg-1",
			Groups:    []string{"tg-1"},
			Source:    "cli",
			Repo:      "github.com/acme/shop",
			Root:      x.root,
			CommonDir: filepath.Join(x.root, ".git"),
		}}
	if _, err := state.SaveMachine(
		x.d.Machine,
		state.KindSchedule,
		state.ScheduleBody{Entries: []state.ScheduleEntry{e}},
		0,
	); err != nil {
		t.Fatal(err)
	}
	x.serve()
	until(t, "the entry removed after the first record save", func() bool {
		rec, _ := state.LoadMachine[state.ScheduleBody](x.d.Machine, state.KindSchedule)
		return len(rec.Body.Entries) == 0
	})
	if n := x.tasks.startedCount("g-tg-1"); n != 1 {
		t.Fatalf("%d Task Managers for one group", n)
	}
}

func TestStatusWaitStreamsThenAnswersBlockedWithTheQuestion(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.tasks.script = func(string) func(*tmEnv) {
		return func(e *tmEnv) {
			defer close(e.out)
			for c := range e.cmds {
				if c.Kind == task.CmdStart {
					e.send(task.Message{
						Kind:       task.MsgEscalate,
						Escalation: &task.Escalation{ID: "e", Group: e.spec.ID, Kind: task.EscBlocked, Reason: "setup"},
					})
				}
			}
		}
	}
	x.serve()
	c, err := Dial(x.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if reps := roundTrip(t, c, x.runReq("q1", "run-1", "tg-1")); reps[len(reps)-1].Status != StatusOK {
		t.Fatalf("%+v", reps)
	}
	reps := roundTrip(t, c, Request{ID: "w1", Verb: VerbStatus, Root: x.root, Flags: Flags{Wait: true, TimeoutS: 5}})
	var sd StatusData
	_ = json.Unmarshal(reps[len(reps)-1].Data, &sd)
	if sd.Waited != "blocked" || len(sd.Blocked) != 1 || sd.Blocked[0].Reason != "setup" || sd.Blocked[0].Question == "" {
		t.Fatalf("final status %+v", sd)
	}
}

func TestStatusWaitTimesOutOnAHealthyRun(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle)
	x.serve()
	if reps := x.ask(x.runReq("q1", "run-1", "tg-1")); reps[len(reps)-1].Status != StatusOK {
		t.Fatalf("%+v", reps)
	}
	x.waitStage("tg-1", StageBuild)
	start := time.Now()
	reps := x.ask(Request{ID: "w1", Verb: VerbStatus, Root: x.root, Flags: Flags{Wait: true, TimeoutS: 1}})
	var sd StatusData
	_ = json.Unmarshal(reps[len(reps)-1].Data, &sd)
	if sd.Waited != "timeout" || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("waited %q after %v", sd.Waited, time.Since(start))
	}
	if len(reps) < 2 {
		t.Fatalf("no streamed status before the final: %d frames", len(reps))
	}
}

func TestReplayStreamsEachEventThenTheGapCount(t *testing.T) {
	evs := []bus.Event{
		{
			Type:    bus.TypeRunComplete,
			Subtype: "done",
			Run:     "run-9",
			Time:    t0,
			Data:    &bus.RunDone{Done: []bus.ID{"g"}, Blocked: []bus.ID{}},
		},
		{Type: bus.TypeRunComplete, Subtype: "stopped", Run: "run-9", Time: t0, Data: &bus.RunStopped{Saved: []bus.ID{}}},
	}
	x := newEng(
		t,
		[][]string{{"tg-1"}},
		func(r *rig) { r.d.Reader = fakeReader{bus.ReplayLog{Events: evs, Gaps: []bus.Gap{{}}}} },
	)
	x.serve()
	reps := x.ask(Request{ID: "r1", Verb: VerbReplay, Target: "run-9"})
	if len(reps) != 3 || reps[0].Status != StatusEvent || reps[1].Event == nil || reps[2].Status != StatusOK {
		t.Fatalf("frames %+v", reps)
	}
	var d struct {
		Gaps int `json:"gaps"`
	}
	_ = json.Unmarshal(reps[2].Data, &d)
	if d.Gaps != 1 {
		t.Fatalf("gaps %d", d.Gaps)
	}
}

func TestCleanPassesDryRunAndTheRepositoriesWithAnOpenRun(t *testing.T) {
	j := &fakeJanitor{}
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle, func(r *rig) { r.d.Janitor = j })
	x.serve()
	x.ask(x.runReq("q1", "run-1", "tg-1"))
	x.waitStage("tg-1", StageBuild)
	reps := x.ask(Request{ID: "c1", Verb: VerbClean, Flags: Flags{DryRun: true}})
	var d struct {
		Removed []CleanItem `json:"removed"`
	}
	_ = json.Unmarshal(reps[len(reps)-1].Data, &d)
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(d.Removed) != 1 || !j.dry || !j.open["github.com/acme/shop"] {
		t.Fatalf("removed %+v dry %v open %v", d.Removed, j.dry, j.open)
	}
}

func TestApproveOnAGroupThatIsNotBlockedIsRefusedNotBlocked(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle)
	x.serve()
	x.ask(x.runReq("q1", "run-1", "tg-1"))
	x.waitStage("tg-1", StageBuild)
	req := Request{ID: "a1", Verb: VerbRun, Target: "tg-1", Flags: Flags{Approve: true},
		Event: &Event{
			Type: bus.TypeRunContinue,
			Time: t0,
			Data: &bus.RunContinue{Target: "tg-1", Answer: "approve", Request: "a1"},
		}}
	reps := x.ask(req)
	last := reps[len(reps)-1]
	if last.Status != StatusRefused || last.Code != CodeNotBlocked {
		t.Fatalf("reply %+v", last)
	}
	reps = x.ask(Request{ID: "a2", Verb: VerbRun, Target: "nope", Flags: Flags{Deny: true},
		Event: &Event{
			Type: bus.TypeRunContinue,
			Time: t0,
			Data: &bus.RunContinue{Target: "nope", Answer: "deny", Request: "a2"},
		}})
	if last := reps[len(reps)-1]; last.Code != CodeUnknownTarget {
		t.Fatalf("reply %+v", last)
	}
}

func TestRefusedRunGivesRunRefusedAndAbandonGivesTheRunId(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1", "tg-2"}, {"tg-2", "tg-1"}, {"tg-3"}}, onlyIdle)
	x.serve()
	reps := x.ask(x.runReq("q1", "run-1", "tg-1", "tg-2"))
	if last := reps[len(reps)-1]; last.Status != StatusRefused || last.Code != CodeRunRefused {
		t.Fatalf("cycle reply %+v", last)
	}
	x.ask(x.runReq("q2", "run-2", "tg-3"))
	x.waitStage("tg-3", StageBuild)
	reps = x.ask(Request{ID: "ab", Verb: VerbAbandon, Target: "tg-3"})
	var d struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(reps[len(reps)-1].Data, &d)
	if d.RunID != "run-2" {
		t.Fatalf("abandon reply %+v", reps[len(reps)-1])
	}
}

func TestRunWithoutAnEventIsABadFrame(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	reps := x.ask(Request{ID: "q1", Verb: VerbRun, Target: "tg-1"})
	if last := reps[len(reps)-1]; last.Code != CodeBadFrame {
		t.Fatalf("reply %+v", last)
	}
}

func TestConnPrinterWritesAFaultToTheCommandInFlightOnly(t *testing.T) {
	p := &ConnPrinter{}
	if err := p.PrintError(fault.Fault{Code: "x", Reason: "nobody listens"}); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	end := p.attach(&connWriter{c: a}, "req-7")
	got := make(chan Reply, 1)
	go func() {
		var r Reply
		_ = json.NewDecoder(b).Decode(&r)
		got <- r
	}()
	if err := p.PrintError(fault.Fault{Code: "config", Reason: "bad value"}); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.ID != "req-7" || r.Status != StatusError || r.Code != "config" || r.Detail != "bad value" {
		t.Fatalf("reply %+v", r)
	}
	end()
	if err := p.PrintError(fault.Fault{Code: "x", Reason: "after"}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineWritesItsHeartbeatFile(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, func(r *rig) { r.d.Timing.EngineBeatEvery = 20 * time.Millisecond })
	x.serve()
	var first struct {
		PID int       `json:"pid"`
		At  time.Time `json:"at"`
	}
	path := filepath.Join(filepath.Dir(x.sock), BeatName)
	until(t, "the beat file", func() bool {
		b, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(b, &first) == nil
	})
	if first.PID != os.Getpid() || first.At.IsZero() {
		t.Fatalf("beat %+v", first)
	}
	info, _ := os.Stat(path)
	until(t, "the beat file rewritten", func() bool {
		i, err := os.Stat(path)
		return err == nil && i.ModTime().After(info.ModTime())
	})
}

func TestSignalStopsEveryRunAndSavesBeforeTheEngineExits(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}}, onlyIdle)
	x.serve()
	x.ask(x.runReq("q1", "run-1", "tg-1"))
	x.waitStage("tg-1", StageBuild)
	x.stop()
	if err := x.exited(); err != nil {
		t.Fatal(err)
	}
	if x.ev.count(bus.TypeRunComplete, "stopped") != 1 {
		until(t, "run-complete stopped", func() bool { return x.ev.count(bus.TypeRunComplete, "stopped") == 1 })
	}
	if got := x.stageOf("tg-1"); got != StageBuild {
		t.Fatalf("stage after the stop %q", got)
	}
}

// A second engine probes the socket and a CLI may dial and leave; neither sends a request, so neither starts the exit rule.
func TestAConnectionThatSendsNoRequestDoesNotStartTheExitRule(t *testing.T) {
	x := newEng(t, [][]string{{"tg-1"}})
	x.serve()
	x.release()
	select {
	case err := <-x.served:
		t.Fatalf("a probe that sent no request ended the engine: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	x.ask(Request{ID: "s1", Verb: VerbStatus, Root: x.root})
	if err := x.exited(); err != nil {
		t.Fatalf("the engine did not exit after its first request: %v", err)
	}
}

// roundTrip sends a request and reads replies until the final one.
func roundTrip(t *testing.T, c *Conn, req Request) []Reply {
	t.Helper()
	if err := c.Send(req); err != nil {
		t.Fatal(err)
	}
	var out []Reply
	for {
		rep, err := c.Recv()
		if err != nil {
			t.Fatalf("read reply: %v", err)
		}
		out = append(out, rep)
		if rep.Final {
			return out
		}
	}
}
