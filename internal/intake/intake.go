package intake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// PlanRequest is the data of a plan event.
type PlanRequest struct {
	Goal string   `json:"goal"` // the one-line goal; may be empty when Docs is set
	Docs []string `json:"docs"` // source documents to plan from, as repository paths
	Epic int      `json:"epic"` // an existing epic to amend as a delta; 0 for a new epic
}

// EventKey binds PlanRequest to plan / request.
func (PlanRequest) EventKey() bus.Key { return bus.Key{Type: bus.TypePlan, Subtype: "request"} }

// PlanBlocked is the data of error / plan-blocked: the third draft failed lint.
type PlanBlocked struct {
	Findings  []Finding `json:"findings"`   // the findings of the third draft
	DraftPath string    `json:"draft_path"` // folder holding the last failing draft; empty when it could not be saved
}

// EventKey binds PlanBlocked to error / plan-blocked.
func (PlanBlocked) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "plan-blocked"} }

// PlanTimeout is the data of error / plan-timeout: the planner's wall clock ran out.
type PlanTimeout struct {
	WallMin int `json:"wall_min"` // the minutes the plan was allowed
}

// EventKey binds PlanTimeout to error / plan-timeout.
func (PlanTimeout) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "plan-timeout"} }

// PlanInput is the data of error / plan-input: a document, a lock check or the planner session failed before any file was written.
type PlanInput struct {
	Reason string `json:"reason"` // what failed, in one sentence
}

// EventKey binds PlanInput to error / plan-input.
func (PlanInput) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "plan-input"} }

// PlanWrite is the data of error / plan-write: the task-list folders could not be written.
type PlanWrite struct {
	Reason string `json:"reason"` // what failed, in one sentence
}

// EventKey binds PlanWrite to error / plan-write.
func (PlanWrite) EventKey() bus.Key { return bus.Key{Type: bus.TypeError, Subtype: "plan-write"} }

// RegisterEvents binds the plan request and the four plan error subtypes to their data structs.
func RegisterEvents() {
	bus.Register(PlanRequest{}.EventKey(), func() bus.Payload { return &PlanRequest{} })
	bus.Register(PlanBlocked{}.EventKey(), func() bus.Payload { return &PlanBlocked{} })
	bus.Register(PlanTimeout{}.EventKey(), func() bus.Payload { return &PlanTimeout{} })
	bus.Register(PlanInput{}.EventKey(), func() bus.Payload { return &PlanInput{} })
	bus.Register(PlanWrite{}.EventKey(), func() bus.Payload { return &PlanWrite{} })
}

// Publisher is the part of the Event Bus the Intake Handler uses.
type Publisher interface {
	Publish(e bus.Event) error
	Subscribe(name string, types ...bus.Type) (*bus.Subscription, error)
}

// ContextSource is the part of the Context Manager the Intake Handler calls.
type ContextSource interface {
	GetContext(ctx context.Context, req contextmgr.Request) (contextmgr.Context, error)
}

// LockReader answers whether a group is in a run; the State Manager backs it.
type LockReader interface {
	InRun(groupID string) (run string, locked bool, err error)
}

// PlannerLimits are the planner's clocks from the Agent Registry.
type PlannerLimits struct {
	WallMin int // most minutes one plan may run; 0 means no deadline
	IdleMin int // minutes of silence before the session is stopped; the session adapter enforces it
}

// Deps are the collaborators the harness passes in at construction.
type Deps struct {
	Bus       Publisher                                          // publishes events and subscribes by type
	Context   ContextSource                                      // GetContext() of the Context Manager
	Start     StartSession                                       // opens a planner session through the model client
	Locks     LockReader                                         // reports whether a group is in a run
	Protected func() []string                                    // paths no agent may write, from the Task Manager's guard data
	Limits    PlannerLimits                                      // wall clock and returns, from the Agent Registry
	Root      string                                             // repository root
	Tree      func(ctx context.Context) (contextmgr.Tree, error) // read-only view of the base commit
	Chain     []contextmgr.Candidate                             // the planner's link chain with measured contexts; sizes the repo map
	Repo      string                                             // repository id for run-start
	CommonDir string                                             // git common directory for run-start
}

// Handler turns plan requests into task lists and a run-start event.
type Handler struct {
	d        Deps
	mu       sync.Mutex                          // one plan at a time
	wallUnit time.Duration                       // length of one WallMin; a test shortens it
	rename   func(oldPath, newPath string) error // moves a folder into or out of place; a test fails it
	stateMu  sync.Mutex                          // guards current
	current  *planState                          // the plan in progress, for the lint_backlog tool
}

// planState is what lint needs about the plan in progress.
type planState struct {
	existing []Epic
	tracked  []string
}

// New returns a Handler; it does not subscribe until Run is called.
func New(d Deps) *Handler {
	return &Handler{d: d, wallUnit: time.Minute, rename: osRename}
}

// Run subscribes to plan events and handles them one at a time until ctx ends.
func (h *Handler) Run(ctx context.Context) error {
	sub, err := h.d.Bus.Subscribe("intake", bus.TypePlan)
	if err != nil {
		return fmt.Errorf("subscribe to plan events: %w", err)
	}
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			req, ok := ev.Data.(*PlanRequest)
			if !ok {
				continue
			}
			if err := h.handle(ctx, ev.Run, *req); err != nil {
				return err
			}
		}
	}
}

// handle plans one request and publishes run-start on success or the error event the failure maps to.
// It returns an error only when the bus refuses a publish.
func (h *Handler) handle(ctx context.Context, run bus.ID, req PlanRequest) error {
	res, err := h.Plan(ctx, req)
	if err == nil {
		return h.d.Bus.Publish(bus.Event{Type: bus.TypeRunStart, Run: newID(), Data: &bus.RunStart{
			Target: fmt.Sprintf("ep-%d", res.Epic), Groups: res.Groups, Source: "intake",
			Repo: h.d.Repo, Root: h.d.Root, CommonDir: h.d.CommonDir,
		}})
	}
	if ctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	data := faultData(err, h.d.Limits.WallMin)
	return h.d.Bus.Publish(bus.Event{Type: bus.TypeError, Subtype: data.EventKey().Subtype, Run: run, Data: data})
}

// faultData maps a failed plan to the data of its error event.
func faultData(err error, wallMin int) bus.Payload {
	var blocked PlanBlockedError
	var write writeError
	switch {
	case errors.As(err, &blocked):
		return &PlanBlocked{Findings: blocked.Findings, DraftPath: blocked.DraftPath}
	case errors.Is(err, context.DeadlineExceeded):
		return &PlanTimeout{WallMin: wallMin}
	case errors.As(err, &write):
		return &PlanWrite{Reason: write.Error()}
	}
	return &PlanInput{Reason: err.Error()}
}

// newID returns a version 7 UUID: 48 bits of milliseconds, then random bits.
func newID() bus.ID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	b[6] = 0x70 | b[6]&0x0f
	b[8] = 0x80 | b[8]&0x3f
	h := hex.EncodeToString(b[:])
	return bus.ID(h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32])
}
