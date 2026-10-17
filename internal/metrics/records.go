// Package metrics is the Metrics Service: it follows every bus event and keeps the run figures,
// which it rebuilds from the bus's event store.
package metrics

import (
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// The outcomes a group can hold.
const (
	outcomeRunning = "running"
	outcomeDone    = "done"
	outcomeBlocked = "blocked"
	outcomePaused  = "paused"
	// A group ends as done, blocked, stopped, failed, abandoned or interrupted.
	outcomeStopped     = "stopped"
	outcomeFailed      = "failed"
	outcomeAbandoned   = "abandoned"
	outcomeInterrupted = "interrupted"
)

// FaultRecord is a failure of the Metrics Service itself, kept in the figures of the run it
// touched.
type FaultRecord struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // write, trim, read, no_common_dir or no_repository
	Text string    `json:"text"`
}

// SessionRecord is one line of sessions.jsonl: what one model session was and cost.
type SessionRecord struct {
	Session   bus.ID    `json:"session"`
	Run       bus.ID    `json:"run"`
	Group     bus.ID    `json:"group"`
	Agent     string    `json:"agent"`
	Task      string    `json:"task,omitempty"`
	Link      string    `json:"link"` // claude-cli or anthropic-api
	Model     string    `json:"model"`
	Stage     string    `json:"stage"`               // the stage the session ran in
	Reason    string    `json:"reason"`              // the exit reason
	StartedAt time.Time `json:"started_at,omitzero"` // zero when the session end had no start
	// the time of session.end; retention ages a record by it
	EndedAt time.Time         `json:"ended_at,omitzero"`
	WallS   int64             `json:"wall_s"` // wall time in seconds
	Usage   *bus.SessionUsage `json:"usage,omitempty"`
}

// StageTime is the time a group spent in one stage.
type StageTime struct {
	Stage string `json:"stage"`
	// how often the group entered it; more than 1 after a return to Build
	Entries int   `json:"entries"`
	Seconds int64 `json:"seconds"`
}

// GroupMetrics is one entry of groups.json: the figures for one group.
type GroupMetrics struct {
	Group     bus.ID    `json:"group"`
	Run       bus.ID    `json:"run"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"` // zero while the group is open
	// running, paused, blocked, done, stopped, failed, abandoned or interrupted
	Outcome   string           `json:"outcome"`
	Stages    []StageTime      `json:"stages"`
	PausedS   int64            `json:"paused_s"`  // seconds in the holding state paused
	BlockedS  int64            `json:"blocked_s"` // seconds in the holding state blocked
	Sessions  int              `json:"sessions"`
	TasksDone int              `json:"tasks_done"`
	Retries   int              `json:"retries"` // task.retry events
	Returns   int              `json:"returns"` // stage.enter events with return true
	Usage     bus.SessionUsage `json:"usage"`   // the sum over the group's sessions
}

// RunMetrics is one entry of runs.json: the figures for one run.
type RunMetrics struct {
	Run       bus.ID           `json:"run"`
	StartedAt time.Time        `json:"started_at"`
	EndedAt   time.Time        `json:"ended_at,omitzero"`
	WallS     int64            `json:"wall_s"`
	Groups    int              `json:"groups"`
	Done      int              `json:"done"`
	Blocked   int              `json:"blocked"`
	Faults    int              `json:"faults"`   // error events with severity error
	Warnings  int              `json:"warnings"` // error events with severity warn
	Usage     bus.SessionUsage `json:"usage"`
	Dir       string           `json:"dir"` // the metrics folder of the run's repository
	// the Service's own failures that touched this run, each once
	Failures []FaultRecord `json:"failures"`
}

// age returns the time a record is aged by: its end, or its start when it has no end.
func (r SessionRecord) age() time.Time {
	if !r.EndedAt.IsZero() {
		return r.EndedAt
	}

	return r.StartedAt
}

// addUsage returns a plus b, field by field.
func addUsage(a, b bus.SessionUsage) bus.SessionUsage {
	return bus.SessionUsage{
		TokensIn: a.TokensIn + b.TokensIn, TokensOut: a.TokensOut + b.TokensOut,
		CacheRead: a.CacheRead + b.CacheRead, CacheWrite: a.CacheWrite + b.CacheWrite,
		USD: a.USD + b.USD, USDEquiv: a.USDEquiv + b.USDEquiv,
		SkillTokens: a.SkillTokens + b.SkillTokens,
	}
}
