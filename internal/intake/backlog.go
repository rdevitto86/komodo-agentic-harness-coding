// Package intake turns a request to plan into task lists: it reads the task-list files, lints them, runs the planner session and writes a plan that passed lint.
package intake

// Priority is a group's ordering hint.
type Priority string

// The two priorities a TG.md may name.
const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
)

// Epic is one docs/backlog/epic-N/ folder.
type Epic struct {
	Number int     // N in epic-N
	Groups []Group // one per tg-N.M folder, in folder-name order
}

// Group is one tg-N.M folder: TG.md plus its task files.
type Group struct {
	ID           string       // identifier from id:
	Name         string       // folder name, such as tg-1.2
	Goal         string       // one-line goal
	Points       int          // story points
	Status       string       // status: as last written
	Subsystem    string       // component name, or none
	Touches      []string     // globs the group expects to change
	Priority     Priority     // high or normal
	Files        []string     // files the builder may write
	Dependencies []string     // names of groups that must finish first
	Acceptance   []Acceptance // checks the tester runs
	Tasks        []Task       // at most 8
}

// Acceptance is one acceptance item.
type Acceptance struct {
	Keyword string   // WHEN, IF or WHILE
	Text    string   // the item after the keyword, through its SHALL clause
	Cmd     string   // optional command the tester runs
	Needs   []string // optional: network, docker or service:<name>
}

// Task is one tsk-*.md file.
type Task struct {
	Name          string   // file name without .md
	Files         []string // files the task names, at most 5
	DoneWhen      string   // command that proves the task done
	EstimateLines int      // planner's estimate of changed lines, at most 300
	Card          string   // the task's own write-up after the field block
}
