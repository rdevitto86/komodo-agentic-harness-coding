package dashboard

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Run is the header of the view: which run, in which repo, for which epic.
type Run struct {
	ID      string    `json:"id"`      // run id
	Repo    string    `json:"repo"`    // repo name
	Epic    string    `json:"epic"`    // epic the run works on
	Started time.Time `json:"started"` // when the run started
}

// Session is one agent session of a group.
type Session struct {
	Status string  `json:"status"`           // idle, running, pass or fail
	Model  string  `json:"model"`            // model of the session
	Tokens int     `json:"tokens,omitempty"` // tokens used so far; zero when tracking is off
	USD    float64 `json:"usd,omitempty"`    // cost so far; zero when tracking is off
}

// Sessions are the three agent sessions a group runs; the planner works once per run, at intake.
type Sessions struct {
	Builder  Session `json:"builder"`  // writes the code
	Tester   Session `json:"tester"`   // checks the delta, in parallel with the reviewer
	Reviewer Session `json:"reviewer"` // approves the work, in parallel with the tester
}

// Group is one task group: where it is, how far along, and who works on it.
type Group struct {
	Group      string   `json:"group"`       // group id
	Label      string   `json:"label"`       // short name of the work
	Stage      string   `json:"stage"`       // one of Stages
	Hold       string   `json:"hold"`        // empty, paused or blocked
	Block      string   `json:"block"`       // reason for the hold, empty when running
	TasksDone  int      `json:"tasks_done"`  // tasks finished
	TasksTotal int      `json:"tasks_total"` // tasks planned
	ElapsedMin int      `json:"elapsed_min"` // minutes since the group started
	Brief      string   `json:"brief"`       // prompt the live session got
	Sessions   Sessions `json:"sessions"`    // builder, tester and reviewer
}

// Event is one entry of the event feed, with the bus event's type and subtype.
type Event struct {
	// run-status, run-complete, run-failure, resource, error and others
	Type    string    `json:"type"`
	Subtype string    `json:"subtype"` // what happened within the type
	Group   string    `json:"group"`   // group the event is about, empty for the run
	At      time.Time `json:"at"`      // when it happened
}

// Link is the plan window of one agent link, as the Resource Manager watches it.
type Link struct {
	Name    string `json:"link"`     // link name
	Percent int    `json:"used_pct"` // share of the plan window used
}

// View is the whole mock view file: the run, its groups, recent events and plan windows.
type View struct {
	Run       Run     `json:"run"`       // run header
	Groups    []Group `json:"groups"`    // one per task group
	Events    []Event `json:"events"`    // any order; the page sorts them
	Resources []Link  `json:"resources"` // one per link
}

// KPIs are the counts the tiles show.
type KPIs struct {
	Running, Blocked, Done int     // groups running, blocked and done
	Tokens                 int     // tokens across all sessions
	USD                    float64 // cost across all sessions
}

// namedSession pairs a session with its agent name.
type namedSession struct {
	Role string // builder, tester or reviewer
	Session
}

// stageBox is one box of the pipeline diagram.
type stageBox struct {
	Key, Title string // stage key and its shown name
	X, Mid     int    // left edge and centre
	Prev       int    // right edge of the box before, where the flow line starts
	Count      int    // groups at this stage
}

// pointer is one dot on the stage diagram.
type pointer struct {
	ID   string // group id
	Tone string // active, blocked, paused or done
	X, Y int    // centre of the stage box and the row of the group
}

// card is one group card ready to draw.
type card struct {
	Group
	Tone   string         // semantic colour
	Chip   string         // stage chip text, or the hold
	Held   bool           // paused or blocked, so the card offers a resume
	Pct    int            // task progress, 0 to 100
	InTest bool           // tester and reviewer run side by side
	Rows   []namedSession // builder, tester and reviewer
	ID     string         // element id suffix
}

// eventRow is one line of the event feed.
type eventRow struct {
	Event
	Time string // clock time
	Tone string // colour class of the type
}

// linkRow is one plan-window meter.
type linkRow struct {
	Link
	Width int    // percent clamped to 0 to 100
	Tone  string // ok, warn or hot
}

var (
	// Stages are the pipeline stages in order, as the diagram draws them.
	Stages = []string{"intake", "build", "test_review", "commit", "cleanup", "done"}

	// stageTitles are the stage names as the page shows them.
	stageTitles = map[string]string{
		"intake":      "Intake",
		"build":       "Build",
		"test_review": "Test · review",
		"commit":      "Commit",
		"cleanup":     "Cleanup",
		"done":        "Done",
	}

	// Agents are the four agents whose model the page offers to change.
	Agents = []string{"planner", "builder", "tester", "reviewer"}

	// Models are the agent `model` values the page offers, as the config agent group names them.
	Models = []string{"opus", "sonnet", "haiku", "opus-prev"}

	// maxEvents caps the event feed.
	maxEvents = 15

	// slugChars matches every character an element id should not hold.
	slugChars = regexp.MustCompile(`[^a-z0-9]+`)
)

// sessions lists a group's sessions in display order.
func (g Group) sessions() []namedSession {
	return []namedSession{
		{"builder", g.Sessions.Builder},
		{"tester", g.Sessions.Tester},
		{"reviewer", g.Sessions.Reviewer},
	}
}

// tone is the semantic colour of a group: blocked, paused, done or active.
func (g Group) tone() string {
	switch {
	case g.Hold == "blocked":
		return "blocked"
	case g.Hold == "paused":
		return "paused"
	case g.Stage == "done":
		return "done"
	}

	return "active"
}

// kpis counts running, blocked and done groups and sums usage over every session.
func kpis(v View) KPIs {
	var k KPIs

	for _, g := range v.Groups {
		switch {
		case g.Hold == "blocked":
			k.Blocked++
		case g.Stage == "done":
			k.Done++
		case g.Hold == "":
			k.Running++
		}

		for _, s := range g.sessions() {
			k.Tokens += s.Tokens
			k.USD += s.USD
		}
	}

	return k
}

// stageIndex is the position of a stage in Stages, Intake when the stage is unknown.
func stageIndex(stage string) int { return max(slices.Index(Stages, stage), 0) }

// stageCounts returns how many groups sit at each stage, held ones included.
func stageCounts(gs []Group) []int {
	counts := make([]int, len(Stages))
	for _, g := range gs {
		counts[stageIndex(g.Stage)]++
	}

	return counts
}

// recentEvents returns the newest maxEvents events, newest first.
func recentEvents(es []Event) []Event {
	out := slices.Clone(es)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })

	if len(out) > maxEvents {
		out = out[:maxEvents]
	}

	return out
}

// stageX is the left edge of stage box i; boxes are 88 wide with 22 between.
func stageX(i int) int { return 1 + i*110 }

// boxes places one box per stage with its live count.
func boxes(counts []int) []stageBox {
	out := make([]stageBox, len(Stages))
	for i, st := range Stages {
		out[i] = stageBox{
			Key:   st,
			Title: stageTitles[st],
			X:     stageX(i),
			Mid:   stageX(i) + 44,
			Prev:  stageX(i) - 22,
			Count: counts[i],
		}
	}

	return out
}

// pointers places each group's dot under its stage box, stacking the dots of one stage 26 apart.
func pointers(gs []Group) []pointer {
	ps := make([]pointer, 0, len(gs))
	stacked := make([]int, len(Stages))

	for _, g := range gs {
		i := stageIndex(g.Stage)
		ps = append(
			ps,
			pointer{ID: g.Group, Tone: g.tone(), X: stageX(i) + 44, Y: 122 + stacked[i]*26},
		)
		stacked[i]++
	}

	return ps
}

// cards builds the group cards.
func cards(gs []Group) []card {
	out := make([]card, 0, len(gs))
	for _, g := range gs {
		c := card{
			Group:  g,
			Tone:   g.tone(),
			Held:   g.Hold != "",
			InTest: g.Stage == "test_review" && g.Hold == "",
			Rows:   g.sessions(),
		}

		c.Chip = stageTitles[g.Stage]
		switch g.Hold {
		case "blocked":
			c.Chip = "Blocked"
		case "paused":
			c.Chip = "Paused"
		}

		if g.TasksTotal > 0 {
			c.Pct = min(max(g.TasksDone*100/g.TasksTotal, 0), 100)
		}

		slug := strings.Trim(slugChars.ReplaceAllString(strings.ToLower(g.Group), "-"), "-")
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(g.Brief))
		c.ID = fmt.Sprintf("brief-%s-%08x", slug, hash.Sum32())
		out = append(out, c)
	}

	return out
}

// eventRows formats the newest events for the feed.
func eventRows(es []Event) []eventRow {
	rows := make([]eventRow, 0, maxEvents)

	for _, e := range recentEvents(es) {
		tone := "plain"

		switch e.Type {
		case "run-status":
			tone = "status"
		case "run-complete":
			tone = "done"
		case "run-failure", "error":
			tone = "blocked"
		case "resource":
			tone = "paused"
		}

		rows = append(rows, eventRow{Event: e, Time: e.At.Local().Format("15:04:05"), Tone: tone})
	}

	return rows
}

// linkRows colours each meter by how much of the window is used.
func linkRows(ls []Link) []linkRow {
	rows := make([]linkRow, 0, len(ls))
	for _, l := range ls {
		tone := "ok"

		switch {
		case l.Percent >= 90:
			tone = "hot"
		case l.Percent >= 75:
			tone = "warn"
		}

		rows = append(rows, linkRow{Link: l, Width: min(max(l.Percent, 0), 100), Tone: tone})
	}

	return rows
}

// num writes n with thousands separators.
func num(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}

	return s
}

// usd writes a cost in dollars.
func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }
