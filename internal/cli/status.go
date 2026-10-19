package cli

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/metrics"
)

// StatusData is the data of reply.status.
type StatusData struct {
	Runs   []RunRow    `json:"runs"`
	Queued []QueuedRow `json:"queued"`
	// groups that wait for an answer
	Blocked []BlockedRow `json:"blocked"`
	// with --wait: blocked, done or timeout
	Waited string `json:"waited,omitempty"`
	// status (id): the run's or group's time, tokens and cost
	Figures *Figures `json:"figures,omitempty"`
	// status (id) of a failed or blocked group: the run's last events
	Explain []bus.Event `json:"explain,omitempty"`
	// status --all: the 20 most recent runs with their totals
	Recent []metrics.RunMetrics `json:"recent,omitempty"`
	// status --all: one row per agent and model over the last 30 days
	Scorecard *metrics.Scorecard `json:"scorecard,omitempty"`
}

// Figures are the time, tokens and cost of one run or group, from the Metrics Service.
type Figures struct {
	WallS     int64   `json:"wall_s"` // seconds from the start to the end, or to now while open
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	USD       float64 `json:"usd"`       // metered dollars
	USDEquiv  float64 `json:"usd_equiv"` // notional dollars of subscription sessions
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
	State string `json:"state"` // the state the Runtime Manager holds the group in
}

// QueuedRow is one entry of the schedule queue.
type QueuedRow struct {
	Target   string    `json:"target"`
	RunID    string    `json:"run_id"` // the run id the engine minted when it queued the entry
	Root     string    `json:"root"`   // the repository root the target belongs to
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

// handleStatus asks the engine for live progress; with --wait it holds until a group is blocked or done.
func handleStatus(c *Call) Result {
	target := ""
	if len(c.Args) == 1 {
		target = c.Args[0]
	}

	wait := flagBool(c, "wait")
	req := c.request(engine.VerbStatus, target)
	if c.Global.All {
		req.Root = ""
	}

	req.Flags = engine.Flags{All: c.Global.All, Wait: wait}
	if wait {
		req.Flags.TimeoutS = int(flagDuration(c, "timeout").Seconds())
	}

	frames, res := c.ask(req)
	if res != nil {
		return *res
	}

	sd := statusFrames(frames, wait)
	if wait && sd.Waited == "blocked" && len(sd.Blocked) > 0 {
		return blockedResult(sd)
	}

	outcome := OutcomeOK
	if wait && sd.Waited == "blocked" {
		outcome = OutcomeHeld
	}

	return Result{
		Outcome: outcome,
		Shape:   "reply.status",
		Facts:   statusFacts(sd),
		Data:    sd,
	}
}

// statusFrames builds the status from the reply frames: the last streamed status, and the final frame's waited value.
func statusFrames(frames []engine.Reply, wait bool) StatusData {
	var sd StatusData
	final := frames[len(frames)-1]

	if !wait {
		decodeFinal(frames, &sd)
	} else {
		for _, f := range frames[:len(frames)-1] {
			if f.Status == engine.StatusOK && len(f.Data) > 0 {
				sd = StatusData{}
				_ = json.Unmarshal(f.Data, &sd)
			}
		}
		var fin StatusData
		if len(final.Data) > 0 {
			_ = json.Unmarshal(final.Data, &fin)
		}
		// The final frame is the state at the moment the wait ended, so its blocked rows win over an earlier frame's.
		if len(fin.Blocked) > 0 {
			sd.Runs, sd.Blocked = fin.Runs, fin.Blocked
		}
		sd.Waited = fin.Waited
	}

	if sd.Runs == nil {
		sd.Runs = []RunRow{}
	}
	if sd.Queued == nil {
		sd.Queued = []QueuedRow{}
	}
	if sd.Blocked == nil {
		sd.Blocked = []BlockedRow{}
	}
	return sd
}

// blockedResult reports the first blocked group's question with exit 3.
func blockedResult(sd StatusData) Result {
	b := sd.Blocked[0]
	if !format.IsBlockReason(b.Reason) {
		return Result{Outcome: OutcomeHeld, Shape: "reply.status", Facts: statusFacts(sd), Data: sd}
	}

	facts := format.NeedsFacts{
		Code: b.Reason, Group: b.Group, Question: b.Question,
		Fix: "komodo run " + b.Group + " --approve or --deny",
	}

	return Result{
		Outcome: OutcomeHeld,
		Shape:   "needs." + b.Reason,
		Code:    b.Reason,
		Facts:   facts,
		Data:    sd,
	}
}

// statusFacts turns the status into the tables reply.status prints.
func statusFacts(sd StatusData) format.StatusFacts {
	f := format.StatusFacts{
		Runs:    format.Table{Head: []string{"Run id", "Label", "Groups"}, What: "runs"},
		Queued:  format.Table{Head: []string{"Label", "Start"}, What: "queued runs"},
		Blocked: format.Table{Head: []string{"Group", "Reason", "Question"}, What: "blocked groups"},
		Waited:  sd.Waited,
	}

	for _, r := range sd.Runs {
		var gs []string
		for _, g := range r.Groups {
			gs = append(gs, g.Group+" ("+g.Stage+")")
		}
		f.Runs.Rows = append(f.Runs.Rows, []string{r.RunID, r.Target, strings.Join(gs, ", ")})
	}
	for _, q := range sd.Queued {
		f.Queued.Rows = append(f.Queued.Rows, []string{q.Target, q.At.Format(time.RFC3339)})
	}
	for _, b := range sd.Blocked {
		f.Blocked.Rows = append(f.Blocked.Rows, []string{b.Group, b.Reason, b.Question})
	}

	return f
}
