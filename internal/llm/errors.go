package llm

import (
	"errors"
	"time"
)

// CapKind names which provider cap was hit.
type CapKind string

// The caps a provider can report.
const (
	CapWindow CapKind = "window" // the 5-hour window
	CapWeekly CapKind = "weekly" // the weekly cap
	CapModel  CapKind = "model"  // a per-model cap
	CapSpend  CapKind = "spend"  // a spend limit the account owner set
	CapQuota  CapKind = "quota"  // a quota error in a response body
)

// ProviderLimit reports a halted session or a refused start; ResetAt is zero when the provider gave
// none.
type ProviderLimit struct {
	Link    Link
	Cap     CapKind
	ResetAt time.Time
	Handle  *ResumeHandle // set when a running session was halted
}

// Sentinel errors an adapter returns; the Task Manager maps them to skips, escalations or exit
// reasons.
var (
	ErrUnavailable  = errors.New("link unavailable")
	ErrUnauthorized = errors.New("link not logged in")
	ErrModelGone    = errors.New("model not found (HTTP 404)")
	ErrBilling      = errors.New("first line shows a paid API key")
	ErrNotYet       = errors.New("link not built")
	ErrStreamClosed = errors.New("stream already closed")
	ErrUnpriced     = errors.New("model has no price in the catalog")
)

// Error implements the error interface.
func (e *ProviderLimit) Error() string { return "provider limit on " + string(e.Link) }
