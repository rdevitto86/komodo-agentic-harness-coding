package format

import (
	"slices"
	"time"
)

// RunFacts feeds reply.run.
type RunFacts struct {
	// the label the command named, such as tg-1.2 or ep-1; empty for run --all
	Group string `json:"group"`
	RunID string `json:"run_id"`
}

// StopFacts feeds reply.stop.
type StopFacts struct {
	RunID string `json:"run_id"`
}

// PlanFacts feeds reply.plan.
type PlanFacts struct {
	Goal string `json:"goal"`
}

// ConfigRow is one setting as config get prints it.
type ConfigRow struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"` // built-in or machine
}

// ConfigGetFacts feeds reply.config.get.
type ConfigGetFacts struct {
	Rows Table `json:"rows"`
}

// ConfigSetFacts feeds reply.config.set.
type ConfigSetFacts struct {
	Batch   string `json:"batch"`   // the audit batch id; empty when nothing changed
	Changed Table  `json:"changed"` // key, old value, new value
}

// InstallFacts feeds reply.install.
type InstallFacts struct {
	DryRun  bool  `json:"dry_run"` // nothing was written
	Writes  int   `json:"writes"`  // rows whose action is not keep; what a dry run would change
	Changes Table `json:"changes"` // file, action, reason
	Probe   Table `json:"probe"`   // one row per model link checked
}

// UpdateFacts feeds reply.update.
type UpdateFacts struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"` // swapped, current or checked
}

// CleanFacts feeds reply.clean.
type CleanFacts struct {
	Removed Table `json:"removed"`
}

// ScheduleFacts feeds reply.schedule.
type ScheduleFacts struct {
	Target string `json:"target"` // the label that was queued
	RunID  string `json:"run_id"`
	At     string `json:"at"` // the start time, RFC 3339
}

// AnswerFacts feeds reply.answer.
type AnswerFacts struct {
	Group  string `json:"group"`
	Answer string `json:"answer"` // approve, deny or override
}

// AbandonFacts feeds reply.abandon.
type AbandonFacts struct {
	Group string `json:"group"`
	RunID string `json:"run_id"`
}

// ReplayFacts feeds reply.replay.
type ReplayFacts struct {
	RunID string   `json:"run_id"`
	Steps []string `json:"steps"` // one formatted line per recorded event, in recorded order
	Gaps  int      `json:"gaps"`  // places where a torn last line was dropped
}

// StatusFacts feeds reply.status.
type StatusFacts struct {
	Runs    Table  `json:"runs"`             // run id, label, groups
	Queued  Table  `json:"queued"`           // label, start time
	Blocked Table  `json:"blocked"`          // group, reason, question
	Waited  string `json:"waited,omitempty"` // with --wait: blocked, done or timeout
}

// DoctorFacts feeds reply.doctor.
type DoctorFacts struct {
	Checks Table `json:"checks"` // check, result, fix
}

// UIFacts feeds reply.ui.
type UIFacts struct {
	Address string `json:"address"` // where the dashboard listens
}

// VersionFacts feeds reply.version.
type VersionFacts struct {
	Version string `json:"version"`
}

// ErrorFacts feeds error.<code>.
type ErrorFacts struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"` // the command that resolves it
	Run    string `json:"run,omitempty"`
	Group  string `json:"group,omitempty"`
}

// NeedsFacts feeds needs.<code>: a group that waits for a person.
type NeedsFacts struct {
	Code     string `json:"code"`
	Group    string `json:"group"`
	Task     string `json:"task,omitempty"`
	Question string `json:"question,omitempty"` // what the blocked group asks, in plain words
	Fix      string `json:"fix"`
}

// EventFacts feeds event.<type> for the events the CLI prints.
type EventFacts struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype"`
	Run     string         `json:"run"`
	Group   string         `json:"group,omitempty"`
	Time    time.Time      `json:"time"`
	Data    map[string]any `json:"data,omitempty"`
}

// OtherFacts feeds the fixed line for the code other.
type OtherFacts struct {
	Path string `json:"path"` // the file that holds the full text
}

// Table is a fact the table function renders.
type Table struct {
	Head []string   `json:"head"` // column labels
	Rows [][]string `json:"rows"` // cells, already formatted
	What string     `json:"what"` // the noun of the overflow line, such as "groups"
}

// noFacts is the fact struct of a shape that prints no values.
type noFacts struct{}

// shapeInfo describes one registered shape.
type shapeInfo struct {
	facts func() any // returns a pointer to a zero fact struct
	coded bool       // the message must carry a code from the list
}

// shapes is the registry of every shape a message may name.
var shapes = buildShapes()

// buildShapes fills the registry: the fixed reply and event shapes, then error.<code> and needs.<reason>.
func buildShapes() map[string]shapeInfo {
	m := map[string]shapeInfo{
		"reply.install":      {facts: func() any { return new(InstallFacts) }},
		"reply.init":         {facts: func() any { return new(noFacts) }},
		"reply.update":       {facts: func() any { return new(UpdateFacts) }},
		"reply.clean":        {facts: func() any { return new(CleanFacts) }},
		"reply.config.get":   {facts: func() any { return new(ConfigGetFacts) }},
		"reply.config.set":   {facts: func() any { return new(ConfigSetFacts) }},
		"reply.plan":         {facts: func() any { return new(PlanFacts) }},
		"reply.run":          {facts: func() any { return new(RunFacts) }},
		"reply.stop":         {facts: func() any { return new(StopFacts) }},
		"reply.schedule":     {facts: func() any { return new(ScheduleFacts) }},
		"reply.answer":       {facts: func() any { return new(AnswerFacts) }},
		"reply.abandon":      {facts: func() any { return new(AbandonFacts) }},
		"reply.replay":       {facts: func() any { return new(ReplayFacts) }},
		"reply.status":       {facts: func() any { return new(StatusFacts) }},
		"reply.doctor":       {facts: func() any { return new(DoctorFacts) }},
		"reply.ui":           {facts: func() any { return new(UIFacts) }},
		"reply.version":      {facts: func() any { return new(VersionFacts) }},
		"event.run-status":   {facts: func() any { return new(EventFacts) }},
		"event.run-complete": {facts: func() any { return new(EventFacts) }},
		"event.run-failure":  {facts: func() any { return new(EventFacts) }},
		"event.resource":     {facts: func() any { return new(EventFacts) }},
		"other":              {facts: func() any { return new(OtherFacts) }},
	}

	for _, c := range allCodes {
		if c != Other {
			m["error."+string(c)] = shapeInfo{
				facts: func() any { return new(ErrorFacts) },
				coded: true,
			}
		}
	}

	for _, r := range blockReasons {
		m["needs."+r] = shapeInfo{facts: func() any { return new(NeedsFacts) }, coded: true}
	}

	return m
}

// shapeNames returns every registered shape name in sorted order.
func shapeNames() []string {
	names := make([]string, 0, len(shapes))
	for n := range shapes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
