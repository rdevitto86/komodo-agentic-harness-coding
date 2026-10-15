package fault

import (
	"context"
	"fmt"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// ResourceError is a structured resource warning or error; the Output Formatter writes its words.
type ResourceError struct {
	Kind      string    `json:"kind"` // window, window_full, overage, spend_run, spend_day, spend_ceiling, group_tokens or limit
	Severity  Severity  `json:"severity"`
	Link      string    `json:"link,omitempty"` // the model link, such as claude-cli
	Run       string    `json:"run,omitempty"`
	Group     string    `json:"group,omitempty"`
	Value     float64   `json:"value"`     // the measured number: percent, dollars or tokens
	Threshold float64   `json:"threshold"` // the threshold it crossed; 0 for overage and limit
	ResetAt   time.Time `json:"reset_at,omitzero"`
}

// Error implements the error interface.
func (e ResourceError) Error() string { return "resource " + string(e.Severity) + ": " + e.Kind }

// ThrowResourceError publishes a resource fault; it returns an error when the fault is not published or no Run loop will print it.
func (h *Handler) ThrowResourceError(ctx context.Context, e ResourceError) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f := resourceFault(e)
	var before uint64
	if h.sub != nil {
		before = h.sub.Dropped()
	}
	if err := h.publish(f, subtypeResource, false); err != nil {
		h.writeStderr(f, "not published: "+err.Error())
		return fmt.Errorf("publish resource fault: %w", err)
	}
	if !h.running.Load() {
		h.writeStderr(f, "not printed: "+errNoRunLoop.Error())
		return fmt.Errorf("print resource fault: %w", errNoRunLoop)
	}
	if h.sub.Dropped() > before {
		h.writeStderr(f, "not printed: "+errDropped.Error())
		return fmt.Errorf("print resource fault: %w", errDropped)
	}
	return nil
}

// resourceFault builds the refused fault that carries a resource warning or error.
func resourceFault(e ResourceError) Fault {
	f := Fault{
		Source:   Source{Component: "resource", Run: bus.ID(e.Run), Group: bus.ID(e.Group)},
		Code:     string(format.OverLimit),
		Kind:     KindRefused,
		Severity: e.Severity,
	}
	if e.Kind == "window_full" || e.Kind == "spend_ceiling" {
		f.Severity = SeverityWarn
	}
	if e.Kind == "window" {
		f.Reason = fmt.Sprintf("plan window %.0f%% used on %s", e.Value, e.Link)
		f.Fix = "wait for the window reset"
		return f
	}
	f.Reason = fmt.Sprintf("%s at %g against %g", e.Kind, e.Value, e.Threshold)
	if e.Link != "" {
		f.Reason += " on " + e.Link
	}
	return f
}
