package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// StatusData is the data of a status reply; the Metrics Service figures are merged in beside these fields.
type StatusData struct {
	Runs    []RunRow     `json:"runs"`
	Queued  []QueuedRow  `json:"queued"`
	Blocked []BlockedRow `json:"blocked"`          // groups that wait for an answer
	Waited  string       `json:"waited,omitempty"` // with --wait: blocked, done or timeout
	Alert   string       `json:"alert,omitempty"`  // one line while the error handler holds new sessions back
	Errors  []ErrorCount `json:"errors,omitempty"` // error counts by type since the engine started
}

// RunRow is one live run and its groups.
type RunRow struct {
	RunID  string     `json:"run_id"`
	Target string     `json:"target"` // the label the run was started with
	Groups []GroupRow `json:"groups"`
}

// GroupRow is one group of a live run.
type GroupRow struct {
	Group string `json:"group"` // the group label
	Stage string `json:"stage"`
	State string `json:"state"` // running, paused, blocked, waiting or done
}

// QueuedRow is one entry of the schedule queue.
type QueuedRow struct {
	Target   string    `json:"target"`
	RunID    string    `json:"run_id"`
	Root     string    `json:"root"`
	At       time.Time `json:"at"`
	QueuedAt time.Time `json:"queued_at"`
}

// BlockedRow is a group that waits for an answer.
type BlockedRow struct {
	Group    string `json:"group"`
	RunID    string `json:"run_id"`
	Reason   string `json:"reason"`   // the block reason code
	Question string `json:"question"` // what the group asks, in plain words
}

// Snapshot returns the live runs of one repository root (every root when it is empty) with their groups and the groups that
// wait for an answer.
func (m *Manager) Snapshot(root string) ([]RunRow, []BlockedRow) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rows, blocked := []RunRow{}, []BlockedRow{}

	for _, r := range m.runs {
		if root != "" && r.start.Root != root {
			continue
		}

		row := RunRow{RunID: r.id, Target: r.start.Target, Groups: []GroupRow{}}
		for _, id := range r.order {
			s := r.specs[id]

			g := r.groups[id]
			if g == nil {
				stage, st := "", "waiting"
				if r.finished[id] {
					stage, st = string(StageDone), "done"
				}
				row.Groups = append(row.Groups, GroupRow{Group: s.Label, Stage: stage, State: st})
				continue
			}

			g.mu.Lock()
			snap := g.snap
			g.mu.Unlock()

			gr := GroupRow{Group: s.Label, Stage: string(workingOf(snap)), State: "running"}
			switch snap.Stage {
			case StagePaused:
				gr.State = "paused"
			case StageBlocked:
				gr.State = "blocked"

				blocked = append(
					blocked,
					BlockedRow{
						Group:    s.Label,
						RunID:    r.id,
						Reason:   string(snap.Block.Reason),
						Question: g.question(snap),
					},
				)
			case StageDone:
				gr.Stage, gr.State = string(StageDone), "done"
			case NoStage:
				gr.Stage, gr.State = "", "waiting"
			}

			row.Groups = append(row.Groups, gr)
		}

		rows = append(rows, row)
	}

	blocked = append(blocked, m.parkedBlockedLocked(root)...)
	slices.SortFunc(rows, func(a, b RunRow) int { return strings.Compare(a.RunID, b.RunID) })
	slices.SortFunc(blocked, func(a, b BlockedRow) int { return strings.Compare(a.Group, b.Group) })
	return rows, blocked
}

// Ready returns a channel that closes once Run listens for run commands.
func (m *Manager) Ready() <-chan struct{} { return m.ready }

// StopAll runs the stop sequence for every live run at once.
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()

	ids := make([]string, 0, len(m.runs))
	for id := range m.runs {
		ids = append(ids, id)
	}

	m.stopped = true
	m.mu.Unlock()

	done := make(chan struct{}, len(ids))
	for _, id := range ids {
		m.spawn(func() {
			_ = m.Stop(ctx, id)
			done <- struct{}{}
		})
	}

	for range ids {
		<-done
	}
}

// firstSaved returns a channel that closes when the run's first group record is saved; it is closed for a run that is not
// live.
func (m *Manager) firstSaved(runID string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.runs[runID]; r != nil {
		return r.firstCh
	}
	c := make(chan struct{})
	close(c)
	return c
}

// reportErr hands an error the Runtime Manager cannot act on to the error reporter.
func (m *Manager) reportErr(run, group string, err error) {
	if m.d.Errors != nil {
		m.d.Errors(run, group, err)
	}
}

// closeAllCaches closes the cache of every repository, as the engine does when it exits.
func (m *Manager) closeAllCaches() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.repos) == 0 {
		return
	}

	clear(m.repos)
	if m.d.Caches != nil {
		_ = m.d.Caches.Close(context.Background())
	}
}

// forget drops the waiter of a command that will not be settled.
func (m *Manager) forget(request string) {
	m.mu.Lock()
	delete(m.expect, request)
	delete(m.persons, request)
	m.mu.Unlock()
}

// question says in plain words what a blocked group waits for.
func (g *group) question(s State) string {
	label := g.spec.Label
	g.mu.Lock()
	ask := g.ask
	g.mu.Unlock()

	switch s.Block.Reason {
	case state.ReasonNeedsScope:
		if ask != nil {
			return fmt.Sprintf(
				"%s asks to change %s outside its files (%s). Approve or deny?",
				label,
				strings.Join(ask.Paths, ", "),
				ask.Reason,
			)
		}
		return label + " asks to change files outside its list. Approve or deny?"
	}

	return fmt.Sprintf("%s is blocked: %s. Fix the cause and run it again.", label, s.Block.Reason)
}
