package lease

import (
	"context"
	"errors"
	"os"
)

// SweepReport counts what one sweep did.
type SweepReport struct {
	LeasesDropped int `json:"leases_dropped"` // records removed because their clone is gone
}

// Sweep drops the records of a repository whose clone is gone, skipping any lease that is still live.
// A record that changed while the sweep ran is left alone.
func (m *Manager) Sweep(ctx context.Context, repo string) (SweepReport, error) {
	var rep SweepReport
	all, err := m.reg.List(ctx, repo)
	if err != nil {
		return rep, err
	}
	now := m.stamp()
	for _, l := range all {
		if l.ClonePath == "" || l.Live(now, m.machine, m.alive) {
			continue
		}
		if _, err := os.Stat(l.ClonePath); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := m.reg.Delete(ctx, l.Repo, l.Group, l.Rev)
		if errors.Is(err, ErrStaleRev) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return rep, err
		}
		rep.LeasesDropped++
	}
	return rep, nil
}
