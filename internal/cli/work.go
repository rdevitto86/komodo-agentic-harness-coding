package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// RunData is the data of reply.run, reply.schedule and reply.answer.
type RunData struct {
	RunID  string     `json:"run_id,omitempty"`
	Queued *QueuedRow `json:"queued,omitempty"` // with --schedule
	Group  string     `json:"group,omitempty"`  // with --approve, --deny or --override
	Answer string     `json:"answer,omitempty"` // approve, deny or override
	Risky  []string   `json:"risky,omitempty"`  // with --approve: the high or critical groups it landed
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
	Plan string `json:"plan,omitempty"` // the plan id the planner wrote
	Path string `json:"path,omitempty"` // the absolute path of plan.md
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

	Plan  string `json:"plan"`  // an existing plan id whose shape changes, or whose group is rewritten; empty for a new plan
	Group string `json:"group"` // with Plan: the one group rewritten from Goal; empty for a shape change

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

	// Set the version field to 7 and the variant field to 10.
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80

	// Write the bytes as 8-4-4-4-12 hex digits.
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
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
	"engine_stopping": fault.KindRefused, "group_failed": fault.KindRefused, "plan_blocked": fault.KindBlocked,
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

// handleRun starts, schedules or answers a run, or rewrites one group from a prompt and then runs it.
func handleRun(c *Call) Result {
	if flagBool(c, "approve") || flagBool(c, "deny") || flagBool(c, "override") {
		return runAnswer(c)
	}

	plan, label := c.Args[0], ""
	if len(c.Args) > 1 {
		label = c.Args[1]
		if res := rewriteGroup(c, plan, label); res != nil {
			return *res
		}
	}

	req := c.request(engine.VerbRun, plan)
	start := &bus.RunStart{
		Target:    plan,
		Groups:    append([]string{}, c.Groups...),
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
		return runSchedule(c, req, plan)
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
		Facts: format.RunFacts{Group: plan, RunID: runID},
		Data:  RunData{RunID: runID},
	}
}

// rewriteGroup sends the prompt after a group label as a plan event for that group and waits until planning ends; a nil
// result means the plan was written and the group can run.
func rewriteGroup(c *Call, plan, label string) *Result {
	pr := c.planRequest(strings.Join(c.Args[2:], " "))
	pr.Plan, pr.Group = plan, label

	d, res := c.plan(pr)
	if res != nil {
		return res
	}
	if d.Plan == "" {
		r := internalErr("planning ended with no plan")
		return &r
	}

	return nil
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

// runAnswer sends a run-continue event that answers a blocked group; --override needs a terminal.
func runAnswer(c *Call) Result {
	answer := "approve"
	switch {
	case flagBool(c, "deny"):
		answer = "deny"
	case flagBool(c, "override"):
		answer = "override"
	}

	minutes := 0
	if answer == "override" {
		var res *Result
		if minutes, res = finalTryMinutes(c); res != nil {
			return *res
		}
	}

	target, groups := c.Args[0], []string{}
	named := target
	if len(c.Args) > 1 {
		groups, named = []string{c.Args[1]}, c.Args[1]
	}

	req := c.request(engine.VerbRun, target)
	req.Flags = engine.Flags{Approve: answer == "approve", Deny: answer == "deny", Override: answer == "override"}
	req.Event = &engine.Event{
		Type: bus.TypeRunContinue, Run: typedRunID(target), Time: now(),
		Data: &bus.RunContinue{Target: target, Groups: groups, Answer: answer, Minutes: minutes, Request: bus.ID(req.ID)},
	}

	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	var landed struct {
		Risky []string `json:"risky"`
	}
	decodeFinal(frames, &landed)

	return Result{
		Shape: "reply.answer",
		Facts: format.AnswerFacts{Group: named, Answer: answer, Risky: landed.Risky},
		Data:  RunData{Group: named, Answer: answer, Risky: landed.Risky},
	}
}

// finalTryMinutes returns the final try's working time: --minutes, else the person's answer to the prompt, whose default
// is limits.group_wall_min.
func finalTryMinutes(c *Call) (int, *Result) {
	if n, set := flagInt(c, "minutes"); set {
		return n, nil
	}

	def := 60
	if it, err := c.cli.deps.Config.GetConfigItem("limits.group_wall_min"); err == nil {
		if v, ok := it.Value.(int); ok {
			def = v
		} else if v, ok := it.Value.(float64); ok {
			def = int(v)
		}
	}

	_, _ = fmt.Fprintf(c.cli.errOut, "Working time for the final try, in minutes [%d]: ", def)
	line, _ := bufio.NewReader(c.cli.in).ReadString('\n')

	text := strings.TrimSpace(line)
	if text == "" {
		return def, nil
	}

	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > engine.FinalWallMaxMin {
		r := usageErr(
			fmt.Sprintf("the final try's working time must be 1 to %d minutes", engine.FinalWallMaxMin),
			"komodo run "+c.Args[0]+" --override --minutes 60",
		)
		return 0, &r
	}

	return n, nil
}

// target returns the plan id and optional group label, or the run id, that the command named.
func (c *Call) target() string { return strings.Join(c.Args, " ") }

// handleStop sends a run-stop event for a plan id, a plan id and label, or a run id.
func handleStop(c *Call) Result {
	target := c.target()
	req := c.request(engine.VerbStop, target)
	req.Event = &engine.Event{
		Type: bus.TypeRunStop, Run: typedRunID(c.Args[0]), Time: now(),
		Data: &bus.RunStop{Target: target, Request: bus.ID(req.ID)},
	}

	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	d := StopData{RunID: string(typedRunID(c.Args[0]))}
	decodeFinal(frames, &d)

	return Result{Shape: "reply.stop", Facts: format.StopFacts{RunID: d.RunID}, Data: d}
}

// handleAbandon sends an abandon control request for one group of a plan.
func handleAbandon(c *Call) Result {
	group := c.Args[1]
	frames, res := c.ask(c.request(engine.VerbAbandon, c.target()))
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
	frames, res := c.ask(c.request(engine.VerbReplay, c.target()))
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

// handlePlan sends a plan event and waits until planning ends; the reply carries the plan id and the path of plan.md.
func handlePlan(c *Call) Result {
	goal := strings.Join(c.Args, " ")
	pr := c.planRequest(goal)

	// A first word that names a plan, or looks like a plan id, makes the rest a prompt that changes that plan.
	if len(c.Args) > 0 {
		_, err := c.blueprint(c.Args[0])

		switch {
		case err != nil && !planIDShape.MatchString(c.Args[0]):
			// the words are a goal
		case err != nil:
			return unknownPlan(c.Args[0])
		case len(c.Args) < 2:
			return usageErr("plan "+c.Args[0]+" needs a prompt that changes the plan", "komodo plan --help")
		default:
			pr.Plan, pr.Goal = c.Args[0], strings.Join(c.Args[1:], " ")
		}
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

	d, res := c.plan(pr)
	if res != nil {
		return *res
	}

	return Result{
		Shape: "reply.plan",
		Facts: format.PlanFacts{Goal: pr.Goal, Plan: d.Plan, Path: d.Path, Start: "komodo run " + d.Plan},
		Data:  d,
	}
}

// planRequest builds the plan request of a call with the prompt or goal as its text.
func (c *Call) planRequest(goal string) *planRequest {
	return &planRequest{
		Goal:      goal,
		Docs:      []string{},
		Repo:      c.Repo.ID,
		Root:      c.Repo.Root,
		CommonDir: c.CommonDir,
	}
}

// plan sends a plan event and returns the plan id and path its final reply carries; a non-nil result ends the command.
func (c *Call) plan(pr *planRequest) (PlanData, *Result) {
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
		return PlanData{}, res
	}

	for _, f := range frames {
		if f.Status != engine.StatusEvent || f.Event == nil {
			continue
		}
		fl, ok := faultOfEvent(*f.Event)
		if !ok {
			fl, ok = planFault(*f.Event)
		}
		if ok {
			if r, failed := c.cli.faultResult(fl); failed {
				return PlanData{}, &r
			}
		}
	}

	var d PlanData
	decodeFinal(frames, &d)

	return d, nil
}

// planFault turns an error event of the plan, or the refusal of the run it started, into a fault carrying its own reason;
// ok is false for any other event.
func planFault(e bus.Event) (fault.Fault, bool) {
	f := fault.Fault{
		Source:   fault.Source{Component: "intake", Run: e.Run, Group: e.Group},
		Severity: fault.SeverityError,
	}

	switch d := e.Data.(type) {
	case *intake.PlanInput:
		f.Code, f.Kind, f.Reason = string(format.PlanInput), fault.KindUsage, d.Reason
	case *intake.PlanWrite:
		f.Code, f.Kind, f.Reason = string(format.PlanWrite), fault.KindInternal, d.Reason
	case *intake.PlanTimeout:
		f.Code, f.Kind = string(format.PlanTimeout), fault.KindRefused
		f.Reason = fmt.Sprintf("The planner's wall clock of %d minutes ran out", d.WallMin)
	case *intake.PlanLocked:
		f.Code, f.Kind = string(format.Locked), fault.KindRefused
		f.Reason = "The plan tried to rewrite a group that run " + d.Run + " holds"
	case *intake.PlanBlocked:
		f.Code, f.Kind = string(format.PlanBlocked), fault.KindBlocked
		f.Reason = fmt.Sprintf("The third planner draft failed lint with %d findings", len(d.Findings))
		if d.DraftPath != "" {
			f.Reason += "; the last draft is saved in " + d.DraftPath
		}
	default:
		return fault.Fault{}, false
	}

	return f, true
}
