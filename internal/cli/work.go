package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// RunData is the data of reply.run, reply.schedule and reply.answer.
type RunData struct {
	RunID  string     `json:"run_id,omitempty"`
	Queued *QueuedRow `json:"queued,omitempty"` // with --schedule
	Group  string     `json:"group,omitempty"`  // with --approve or --deny
	Answer string     `json:"answer,omitempty"` // approve or deny
}

// StopData is the data of reply.stop.
type StopData struct {
	RunID string `json:"run_id"`
}

// AbandonData is the data of reply.abandon.
type AbandonData struct {
	Group string `json:"group"`
	RunID string `json:"run_id,omitempty"`
}

// PlanData is the data of reply.plan.
type PlanData struct {
	RunID  string   `json:"run_id,omitempty"` // the run the planner started
	Target string   `json:"target,omitempty"` // the epic label the plan wrote
	Groups []string `json:"groups,omitempty"` // the group labels the plan wrote
}

// ReplayData is the data of reply.replay.
type ReplayData struct {
	RunID string      `json:"run_id"` // the attempt that was replayed
	Steps []bus.Event `json:"steps"`  // the recorded events, in recorded order
	Gaps  int         `json:"gaps"`   // places where events are missing: a gap line, or a torn last line that was dropped
}

// planRequest is the data of the plan event the CLI sends; the Intake Handler owns the same shape as PlanRequest.
type planRequest struct {
	Goal string   `json:"goal"` // the one-line goal; may be empty when Docs is set
	Docs []string `json:"docs"` // source documents to plan from, as repository paths
	Epic int      `json:"epic"` // an existing epic to amend as a delta; 0 for a new epic

	Repo      string `json:"repo"`       // the repository id the plan belongs to
	Root      string `json:"root"`       // the repository root on this machine
	CommonDir string `json:"common_dir"` // the repository's git common directory
}

// EventKey binds planRequest to plan / request.
func (planRequest) EventKey() bus.Key { return bus.Key{Type: bus.TypePlan, Subtype: "request"} }

// newID returns a version 7 UUID: 48 bits of milliseconds, then random bits.
func newID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	_, _ = rand.Read(b[6:])

	b[6] = b[6]&0x0f | 0x70                                                       // TODO - we need a small comment here
	b[8] = b[8]&0x3f | 0x80                                                       // TODO - we need a small comment here
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]) // TODO - we need a small comment here
}

// now returns the current UTC time to the second, as an event carries it.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// ask starts the engine when needed, sends one request and returns every frame up to the final one.
func (c *Call) ask(req engine.Request) ([]engine.Reply, *Result) {
	ctx := context.Background()
	eng := c.cli.deps.Engine
	ch, err := eng.Send(ctx, req)

	if err != nil {
		r := internalErr("reach the engine: " + err.Error())
		return nil, &r
	}

	var frames []engine.Reply
	for rep := range ch {
		frames = append(frames, rep)
	}

	if len(frames) == 0 || !frames[len(frames)-1].Final {
		r := internalErr("the engine closed the connection before it finished")
		return nil, &r
	}

	last := frames[len(frames)-1]

	switch last.Status {
	case engine.StatusRefused:
		r := refusedResult(last)
		return nil, &r
	case engine.StatusError:
		r := internalErr("the engine failed: " + last.Detail)
		return nil, &r
	}

	return frames, nil
}

// codeKinds maps each error code a refusal may carry to the fault kind that sets the exit code.
var codeKinds = map[string]fault.Kind{
	"config_tampered": fault.KindRefused, "pinned": fault.KindRefused, "no_terminal": fault.KindRefused,
	"no_repo": fault.KindRefused, "over_limit": fault.KindRefused, "card_changed": fault.KindRefused,
	"locked": fault.KindRefused, "plan_timeout": fault.KindRefused, "context_refused": fault.KindRefused,
	"unknown_target": fault.KindRefused, "run_refused": fault.KindRefused, "not_blocked": fault.KindRefused,
	"engine_stopping": fault.KindRefused, "plan_blocked": fault.KindBlocked,
	"usage": fault.KindUsage, "plan_input": fault.KindUsage, "unknown_key": fault.KindUsage, "bad_value": fault.KindUsage,
	"plan_write": fault.KindInternal, "bad_frame": fault.KindInternal, "internal": fault.KindInternal,
}

// refusedResult maps a refused reply to the result its code's kind sets; an unknown code is an internal failure.
func refusedResult(rep engine.Reply) Result {
	kind, known := codeKinds[rep.Code]
	if !known {
		return internalErr(fmt.Sprintf("the engine refused with the unknown code %q: %s", rep.Code, rep.Detail))
	}

	reason := rep.Detail
	if reason == "" {
		reason = "the engine refused the request"
	}
	return errResult(Outcome(kind.ExitCode()), format.Code(rep.Code), reason, "komodo status")
}

// decodeFinal decodes the data of the last frame into v; absent data leaves v alone.
func decodeFinal(frames []engine.Reply, v any) {
	if d := frames[len(frames)-1].Data; len(d) > 0 {
		_ = json.Unmarshal(d, v)
	}
}

// request builds a Request for a verb with the repository root of the call.
func (c *Call) request(verb, target string) engine.Request {
	return engine.Request{ID: newID(), Verb: verb, Target: target, Root: c.Repo.Root}
}

// typedRunID returns s when it is a run id, and empty when it is a label.
func typedRunID(s string) bus.ID {
	if runIDShape.MatchString(s) {
		return bus.ID(s)
	}
	return ""
}

// handleRun starts, schedules or answers a run.
func handleRun(c *Call) Result {
	if flagBool(c, "approve") || flagBool(c, "deny") {
		return runAnswer(c)
	}

	target, label := "all", ""
	if !c.Global.All {
		target, label = c.Args[0], c.Args[0]
	}

	req := c.request(engine.VerbRun, label)
	req.Flags = engine.Flags{All: c.Global.All}
	start := &bus.RunStart{
		Target:    target,
		Groups:    c.Groups,
		Source:    "cli",
		Repo:      c.Repo.ID,
		Root:      c.Repo.Root,
		CommonDir: c.CommonDir,
		Request:   bus.ID(req.ID),
	}

	if at := flagTime(c, "schedule"); !at.IsZero() {
		start.Schedule = at
		req.Flags.Schedule = at
		req.Event = &engine.Event{ // the engine mints the run id and stores this event
			Type: bus.TypeRunStart,
			Time: now(),
			Data: start,
		}
		return runSchedule(c, req, target)
	}

	runID := newID()
	req.Event = &engine.Event{
		Type: bus.TypeRunStart,
		Run:  bus.ID(runID),
		Time: now(),
		Data: start,
	}

	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	var d struct {
		RunID string `json:"run_id"`
	}
	decodeFinal(frames, &d)

	if d.RunID != "" {
		runID = d.RunID
	}

	return Result{
		Shape: "reply.run",
		Facts: format.RunFacts{Group: label, RunID: runID},
		Data:  RunData{RunID: runID},
	}
}

// runSchedule queues a run; the engine mints the run id and answers with it and the queued time.
func runSchedule(c *Call, req engine.Request, target string) Result {
	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	var d struct {
		RunID  string `json:"run_id"`
		Queued *struct {
			Target   string    `json:"target"`
			RunID    string    `json:"run_id"`
			At       time.Time `json:"at"`
			QueuedAt time.Time `json:"queued_at"`
		} `json:"queued"`
	}

	decodeFinal(frames, &d)
	row := QueuedRow{
		Target: target,
		RunID:  d.RunID,
		Root:   c.Repo.Root,
		At:     req.Flags.Schedule,
	}

	if q := d.Queued; q != nil {
		row.At, row.QueuedAt = q.At, q.QueuedAt
		if row.RunID == "" {
			row.RunID = q.RunID
		}
	}

	facts := format.ScheduleFacts{
		Target: target,
		RunID:  row.RunID,
		At:     row.At.Format(time.RFC3339),
	}

	return Result{
		Shape: "reply.schedule",
		Facts: facts,
		Data:  RunData{RunID: row.RunID, Queued: &row},
	}
}

// runAnswer sends a run-continue event that answers a blocked group.
func runAnswer(c *Call) Result {
	answer := "approve"
	if flagBool(c, "deny") {
		answer = "deny"
	}

	target := c.Args[0]
	req := c.request(engine.VerbRun, target)
	req.Flags = engine.Flags{Approve: answer == "approve", Deny: answer == "deny"}
	req.Event = &engine.Event{
		Type: bus.TypeRunContinue, Run: typedRunID(target), Time: now(),
		Data: &bus.RunContinue{Target: target, Answer: answer, Request: bus.ID(req.ID)},
	}

	if _, res := c.ask(req); res != nil {
		return *res
	}
	return Result{
		Shape: "reply.answer",
		Facts: format.AnswerFacts{Group: target, Answer: answer},
		Data:  RunData{Group: target, Answer: answer},
	}
}

// handleStop sends a run-stop event for a label or a run id.
func handleStop(c *Call) Result {
	target := c.Args[0]
	req := c.request(engine.VerbStop, target)
	req.Event = &engine.Event{
		Type: bus.TypeRunStop, Run: typedRunID(target), Time: now(),
		Data: &bus.RunStop{Target: target, Request: bus.ID(req.ID)},
	}

	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	d := StopData{RunID: string(typedRunID(target))}
	decodeFinal(frames, &d)

	return Result{Shape: "reply.stop", Facts: format.StopFacts{RunID: d.RunID}, Data: d}
}

// handleAbandon sends an abandon control request for one group.
func handleAbandon(c *Call) Result {
	group := c.Args[0]
	frames, res := c.ask(c.request(engine.VerbAbandon, group))
	if res != nil {
		return *res
	}

	d := AbandonData{Group: group}
	decodeFinal(frames, &d)

	return Result{
		Shape: "reply.abandon",
		Facts: format.AbandonFacts{Group: group, RunID: d.RunID},
		Data:  d,
	}
}

// handleReplay collects the recorded events of a run or group and the gap count.
func handleReplay(c *Call) Result {
	frames, res := c.ask(c.request(engine.VerbReplay, c.Args[0]))
	if res != nil {
		return *res
	}

	d := ReplayData{RunID: string(typedRunID(c.Args[0])), Steps: []bus.Event{}}
	var lines []string

	for _, f := range frames {
		if f.Status == engine.StatusEvent && f.Event != nil {
			d.Steps = append(d.Steps, *f.Event)
			lines = append(lines, eventLine(*f.Event))
			if d.RunID == "" {
				d.RunID = string(f.Event.Run)
			}
		}
	}

	var fin struct {
		Gaps int `json:"gaps"`
	}
	decodeFinal(frames, &fin)
	d.Gaps = fin.Gaps

	return Result{
		Shape: "reply.replay",
		Facts: format.ReplayFacts{RunID: d.RunID, Steps: lines, Gaps: d.Gaps},
		Data:  d,
	}
}

// handlePlan sends a plan event and waits for the run-start that ends planning, or for an error event.
func handlePlan(c *Call) Result {
	pr := &planRequest{
		Goal:      strings.Join(c.Args, " "),
		Docs:      []string{},
		Repo:      c.Repo.ID,
		Root:      c.Repo.Root,
		CommonDir: c.CommonDir,
	}

	if from := flagString(c, "from"); from != "" {
		if !filepath.IsAbs(from) {
			from = filepath.Join(c.dir(), from)
		}
		rel, err := filepath.Rel(c.Repo.Root, from)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return usageErr("--from must name a path inside the repository "+c.Repo.Root, "komodo plan --help")
		}
		pr.Docs = append(pr.Docs, filepath.ToSlash(rel))
	}

	req := c.request(engine.VerbPlan, "")
	req.Event = &engine.Event{
		Type:    bus.TypePlan,
		Subtype: "request",
		Run:     bus.ID(newID()),
		Time:    now(),
		Data:    pr,
	}
	frames, res := c.ask(req)

	if res != nil {
		return *res
	}

	d := PlanData{}

	for _, f := range frames {
		if f.Status != engine.StatusEvent || f.Event == nil {
			continue
		}
		if rs, ok := f.Event.Data.(*bus.RunStart); ok && rs.Source == "intake" {
			d = PlanData{RunID: string(f.Event.Run), Target: rs.Target, Groups: rs.Groups}
		}
		if fl, ok := faultOfEvent(*f.Event); ok {
			if r, failed := c.cli.faultResult(fl); failed {
				return r
			}
		}
	}

	return Result{
		Shape: "reply.plan",
		Facts: format.PlanFacts{Goal: pr.Goal},
		Data:  d,
	}
}
