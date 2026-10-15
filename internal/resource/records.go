package resource

import (
	"context"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// WindowRecord is one window's entry in providers.json.
type WindowRecord struct {
	PercentUsed float64   `json:"percent_used"`
	ResetAt     time.Time `json:"reset_at"`
	Sessions    int       `json:"sessions"` // sessions finished in the window
}

// sameRecord reports whether two records hold the same figures and the same instant.
func sameRecord(a, b WindowRecord) bool {
	return a.PercentUsed == b.PercentUsed && a.Sessions == b.Sessions && a.ResetAt.Equal(b.ResetAt)
}

// machineRecords adapts the State Manager's machine-wide record to Records.
type machineRecords struct{ m *state.Machine }

// NewRecords returns the Records that reads and writes window records through m.
func NewRecords(m *state.Machine) Records { return machineRecords{m} }

// LoadWindows returns every stored window record.
func (r machineRecords) LoadWindows(ctx context.Context) (map[WindowKey]WindowRecord, error) {
	stored, err := r.m.LoadWindows(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[WindowKey]WindowRecord, len(stored))
	for k, v := range stored {
		out[WindowKey{Link: k.Link, Window: k.Window}] = WindowRecord{PercentUsed: v.PercentUsed, ResetAt: v.ResetAt, Sessions: v.Sessions}
	}
	return out, nil
}

// SaveWindow stores one window record.
func (r machineRecords) SaveWindow(ctx context.Context, k WindowKey, rec WindowRecord) error {
	return r.m.SaveWindow(ctx, state.WindowKey{Link: k.Link, Window: k.Window},
		state.WindowRecord{PercentUsed: rec.PercentUsed, ResetAt: rec.ResetAt, Sessions: rec.Sessions})
}
