package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// handle answers one request: it runs the verb and sends the replies, the last of which is final.
func (e *Engine) handle(ctx context.Context, w *connWriter, req Request) {
	switch req.Verb {
	case VerbRun:
		e.verbRun(ctx, w, req)
	case VerbStop:
		e.verbStop(ctx, w, req)
	case VerbStatus:
		e.verbStatus(ctx, w, req)
	case VerbAbandon:
		e.verbAbandon(ctx, w, req)
	case VerbReplay:
		e.verbReplay(ctx, w, req)
	case VerbPlan:
		e.verbPlan(ctx, w, req)
	case VerbClean:
		e.verbClean(ctx, w, req)
	case VerbToolList:
		e.verbToolList(w, req)
	case VerbToolCall:
		e.verbToolCall(ctx, w, req)
	default:
		_ = w.send(Reply{
			ID:     req.ID,
			Status: StatusError,
			Code:   CodeBadFrame,
			Detail: "unknown verb " + req.Verb,
			Final:  true,
		})
	}
}

// verbRun starts, queues or answers a run.
func (e *Engine) verbRun(ctx context.Context, w *connWriter, req Request) {
	ev := req.Event
	rc, isAnswer := dataOf[*bus.RunContinue](ev)
	overrideAnswer := isAnswer && rc.Answer == "override"
	if req.Flags.Override || overrideAnswer {
		if ok, err := e.peer(w.c); err != nil || !ok {
			e.fail(w, req.ID, &codeError{CodeNoTerminal, "--override needs a terminal: the requesting process has none"})
			return
		}
	}
	if overrideAnswer != req.Flags.Override && (overrideAnswer || isAnswer) {
		e.badFrame(w, req.ID, "the override flag and the run-continue answer disagree")
		return
	}
	if isAnswer && ev.Type != bus.TypeRunContinue {
		e.badFrame(w, req.ID, "a run answer needs a run-continue event")
		return
	}
	if req.Flags.Approve || req.Flags.Deny || req.Flags.Override {
		if isAnswer {
			rc.Request = bus.ID(req.ID)
			if err := e.await(ctx, req, *ev); err != nil {
				e.fail(w, req.ID, err)
				return
			}
			e.done(w, req.ID, map[string]string{"group": rc.Target, "answer": rc.Answer})
			return
		}

		e.badFrame(w, req.ID, "an answer needs a run-continue event")
		return
	}

	rs, ok := dataOf[*bus.RunStart](ev)
	if !ok || ev.Type != bus.TypeRunStart {
		e.badFrame(w, req.ID, "run needs a run-start event")
		return
	}

	rs.Request = bus.ID(req.ID)
	if !req.Flags.Schedule.IsZero() {
		at := req.Flags.Schedule
		rs.Schedule = at

		ent, err := e.q.add(*rs, req.Target, at)
		if err != nil {
			e.fail(w, req.ID, err)
			return
		}

		e.markSeen()
		e.done(
			w,
			req.ID,
			map[string]any{
				"run_id": ent.RunID,
				"queued": QueuedRow{
					Target:   ent.Target,
					RunID:    ent.RunID,
					Root:     ent.Root,
					At:       ent.At,
					QueuedAt: ent.QueuedAt,
				},
			},
		)

		return
	}

	if ev.Run == "" {
		ev.Run = bus.ID(newID(e.clk.Now()))
	}
	if err := e.await(ctx, req, *ev); err != nil {
		e.fail(w, req.ID, err)
		return
	}

	e.done(w, req.ID, map[string]string{"run_id": string(ev.Run)})
}

// await publishes a command event and waits for the Runtime Manager to settle the request.
func (e *Engine) await(ctx context.Context, req Request, ev Event) error {
	wait := e.m.Expect(req.ID)
	if err := e.d.Bus.Publish(ev); err != nil {
		e.m.forget(req.ID)
		return err
	}

	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		e.m.forget(req.ID)
		return ctx.Err()
	}
}

// fail sends the final reply for an error: a refusal when it carries a code, else an error.
func (e *Engine) fail(w *connWriter, id string, err error) {
	var ce *codeError
	if errors.As(err, &ce) {
		_ = w.send(Reply{
			ID:     id,
			Status: StatusRefused,
			Code:   ce.code,
			Detail: ce.detail,
			Final:  true,
		})
		return
	}

	_ = w.send(Reply{
		ID:     id,
		Status: StatusError,
		Code:   "internal",
		Detail: err.Error(),
		Final:  true,
	})
}

// done sends the final ok reply carrying data.
func (e *Engine) done(w *connWriter, id string, data any) {
	r := Reply{ID: id, Status: StatusOK, Final: true}
	if data != nil {
		r.Data = mustJSON(data)
	}
	_ = w.send(r)
}

// badFrame sends the final reply for a request that lacks what its verb needs.
func (e *Engine) badFrame(w *connWriter, id, detail string) {
	_ = w.send(Reply{ID: id, Status: StatusError, Code: CodeBadFrame, Detail: detail, Final: true})
}

// verbStop stops a live run and removes a queued entry.
func (e *Engine) verbStop(ctx context.Context, w *connWriter, req Request) {
	gone, err := e.q.remove(req.Target)
	if err != nil {
		e.fail(w, req.ID, err)
		return
	}

	runID := e.m.RunOf(req.Target)
	if runID == "" {
		if len(gone) == 0 {
			e.fail(w, req.ID, &codeError{CodeUnknownTarget, "no live or queued run named " + req.Target})
			return
		}
		e.done(w, req.ID, map[string]string{"run_id": gone[0].RunID})
		return
	}

	ev := Event{Type: bus.TypeRunStop, Run: bus.ID(runID), Data: &bus.RunStop{Target: runID, Request: bus.ID(req.ID)}}
	if err := e.await(ctx, req, ev); err != nil {
		e.fail(w, req.ID, err)
		return
	}

	e.done(w, req.ID, map[string]string{"run_id": runID})
}

// verbStatus answers with the live runs and the queue; with Wait it streams until a group is blocked or every group is Done.
func (e *Engine) verbStatus(ctx context.Context, w *connWriter, req Request) {
	if !req.Flags.Wait {
		_ = w.send(Reply{
			ID:     req.ID,
			Status: StatusOK,
			Data:   e.statusReply(ctx, req, e.statusData(ctx, req)),
			Final:  true,
		})
		return
	}

	limit := time.Duration(req.Flags.TimeoutS) * time.Second

	var timeout <-chan time.Time
	if limit > 0 {
		t := time.NewTimer(limit)
		defer t.Stop()
		timeout = t.C
	}

	var last []byte

	for {
		ch := e.m.Changes()

		d := e.statusData(ctx, req)
		switch {
		case len(d.Blocked) > 0:
			d.Waited = "blocked"
		case len(d.Runs) == 0 && len(d.Queued) == 0:
			d.Waited = "done"
		}
		if d.Waited != "" {
			_ = w.send(Reply{ID: req.ID, Status: StatusOK, Data: e.statusReply(ctx, req, d), Final: true})
			return
		}
		if b := mustJSON(d); string(b) != string(last) {
			last = b
			if err := w.send(Reply{ID: req.ID, Status: StatusOK, Data: e.statusReply(ctx, req, d)}); err != nil {
				return
			}
		}

		select {
		case <-ch:
		case <-timeout:
			d.Waited = "timeout"
			_ = w.send(Reply{ID: req.ID, Status: StatusOK, Data: e.statusReply(ctx, req, d), Final: true})
			return
		case <-ctx.Done():
			return
		}
	}
}

// statusReply encodes the status with the Metrics Service figures merged in.
func (e *Engine) statusReply(ctx context.Context, req Request, d StatusData) json.RawMessage {
	b := mustJSON(d)
	if e.d.Extras == nil {
		return b
	}

	extra := e.d.Extras(ctx, req.Target, req.Flags.All)
	if len(extra) == 0 {
		return b
	}

	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, v := range extra {
		m[k] = v
	}

	return mustJSON(m)
}

// statusData builds the status the request asks for.
func (e *Engine) statusData(ctx context.Context, req Request) StatusData {
	root := req.Root
	if req.Flags.All {
		root = ""
	}

	runs, blocked := e.m.Snapshot(root)
	d := StatusData{Runs: runs, Queued: []QueuedRow{}, Blocked: blocked, Alert: e.m.brk.alert()}

	es, _, _ := e.q.load()
	for _, ent := range es {
		if root == "" || ent.Root == root {
			d.Queued = append(
				d.Queued,
				QueuedRow{
					Target:   ent.Target,
					RunID:    ent.RunID,
					Root:     ent.Root,
					At:       ent.At,
					QueuedAt: ent.QueuedAt,
				},
			)
		}
	}

	return d
}

// verbAbandon drops a group for good.
func (e *Engine) verbAbandon(ctx context.Context, w *connWriter, req Request) {
	run, err := e.m.Abandon(ctx, req.Target)
	if err != nil {
		e.fail(w, req.ID, err)
		return
	}
	e.done(w, req.ID, map[string]string{"group": req.Target, "run_id": run})
}

// verbReplay streams the recorded events of a run or group, then the gap count; it calls no model and changes nothing.
func (e *Engine) verbReplay(ctx context.Context, w *connWriter, req Request) {
	if e.d.Reader == nil {
		e.fail(w, req.ID, errors.New("the engine has no event store to replay"))
		return
	}

	run, group, err := e.replayTarget(ctx, req)
	if err != nil {
		e.fail(w, req.ID, err)
		return
	}

	log, err := e.d.Reader.Replay(ctx, run, group)
	if errors.Is(err, os.ErrNotExist) {
		e.fail(w, req.ID, &codeError{CodeUnknownTarget, "no recorded run named " + req.Target})
		return
	}
	if err != nil {
		e.fail(w, req.ID, err)
		return
	}

	for i := range log.Events {
		if err := w.send(Reply{ID: req.ID, Status: StatusEvent, Event: &log.Events[i]}); err != nil {
			return
		}
	}

	gaps := len(log.Gaps)
	if log.Skipped > 0 {
		gaps++
	}

	e.done(w, req.ID, map[string]int{"gaps": gaps})
}

// verbPlan publishes the plan event and streams the planning run's events until the Intake Handler reports an error, or the
// run it started is accepted (run.start) or refused (run-refused).
func (e *Engine) verbPlan(ctx context.Context, w *connWriter, req Request) {
	if req.Event == nil || req.Event.Type != bus.TypePlan {
		e.badFrame(w, req.ID, "plan needs a plan event")
		return
	}

	sub, err := e.d.Bus.Subscribe("plan-"+req.ID, bus.TypeRunStart, bus.TypeRunStatus, bus.TypeError, bus.TypePlan)
	if err != nil {
		e.fail(w, req.ID, err)
		return
	}
	defer sub.Close()

	if err := e.d.Bus.Publish(*req.Event); err != nil {
		e.fail(w, req.ID, err)
		return
	}

	started := bus.ID("") // the run the Intake Handler started, once it has

	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}

			rs, isStart := ev.Data.(*bus.RunStart)
			intake := isStart && rs.Source == "intake"
			if started != "" {
				if ev.Run == started && planRunOutcome(ev) {
					if ev.Type == bus.TypeError {
						_ = w.send(Reply{ID: req.ID, Status: StatusEvent, Event: &ev})
					}
					e.done(w, req.ID, nil)
					return
				}
				continue
			}
			if ev.Type == bus.TypeRunStatus || (isStart && !intake) || (ev.Run != req.Event.Run && !intake) {
				continue
			}
			if err := w.send(Reply{ID: req.ID, Status: StatusEvent, Event: &ev}); err != nil {
				return
			}
			if intake {
				started = ev.Run
			} else if ev.Type == bus.TypeError {
				e.done(w, req.ID, nil)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// planRunOutcome reports whether the event settles a started run: its first run.start or its run-refused.
func planRunOutcome(ev Event) bool {
	return ev.Type == bus.TypeRunStatus && ev.Subtype == "run.start" ||
		ev.Type == bus.TypeError && ev.Subtype == "run-refused"
}

// verbClean removes what belongs to no open run, or lists it with DryRun.
func (e *Engine) verbClean(ctx context.Context, w *connWriter, req Request) {
	items := []CleanItem{}

	if e.d.Janitor != nil {
		open := e.m.OpenRepos()
		if es, _, err := e.q.load(); err == nil {
			for _, ent := range es {
				open[ent.Start.Repo] = true
			}
		}

		got, err := e.d.Janitor.Clean(ctx, req.Flags.DryRun, req.Root, open)
		if err != nil {
			e.fail(w, req.ID, err)
			return
		}

		items = append(items, got...)
	}

	e.done(w, req.ID, map[string]any{"removed": items})
}

// dataOf returns the event's data as T.
func dataOf[T bus.Payload](ev *Event) (T, bool) {
	var zero T
	if ev == nil {
		return zero, false
	}
	d, ok := ev.Data.(T)
	return d, ok
}
