package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/engine"
)

// uiFeedPerRun is how many of a run's newest events the feed keeps; uiFeedTotal caps the merged feed.
const (
	uiFeedPerRun = 50
	uiFeedTotal  = 40
)

// uiState is everything the page draws, in one JSON document.
type uiState struct {
	Runs    []uiRun      `json:"runs"`
	Queued  []QueuedRow  `json:"queued"`
	Blocked []BlockedRow `json:"blocked"`
	Totals  uiTotals     `json:"totals"`
	Events  []uiEvent    `json:"events"`          // newest first
	Error   string       `json:"error,omitempty"` // set when the engine could not be read
}

// uiRun is one live run with its groups and its time, tokens and cost.
type uiRun struct {
	RunID   string    `json:"run_id"`
	Target  string    `json:"target"`
	Groups  []uiGroup `json:"groups"`
	Figures *Figures  `json:"figures,omitempty"` // the figures of status (id); absent when the engine sent none
}

// uiGroup is one group of a run; a blocked group carries what it asks.
type uiGroup struct {
	Group    string `json:"group"`
	Stage    string `json:"stage"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Question string `json:"question,omitempty"`
}

// uiTotals are the counts and sums the tiles show.
type uiTotals struct {
	Running int     `json:"running"`
	Paused  int     `json:"paused"`
	Blocked int     `json:"blocked"`
	Waiting int     `json:"waiting"`
	Done    int     `json:"done"`
	Queued  int     `json:"queued"`
	Tokens  int     `json:"tokens"` // input and output tokens of every shown run
	USD     float64 `json:"usd"`    // metered dollars of every shown run
}

// uiEvent is one line of the event feed.
type uiEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Subtype string    `json:"subtype"`
	Run     string    `json:"run"`
	Group   string    `json:"group"` // empty for a run-level event
}

// mustJSONBytes encodes v; the dashboard's own types always encode.
func mustJSONBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"encode the state"}`)
	}
	return b
}

// uiAsk sends one request and returns the frames up to the final one, or the error that ended it.
func uiAsk(ctx context.Context, eng Engine, req engine.Request) ([]engine.Reply, error) {
	ch, err := eng.Send(ctx, req)
	if err != nil {
		return nil, err
	}
	var frames []engine.Reply
	for {
		select {
		case rep, ok := <-ch:
			if !ok {
				if len(frames) == 0 || !frames[len(frames)-1].Final {
					return nil, errors.New("the engine closed the connection before it finished")
				}
				return frames, nil
			}
			frames = append(frames, rep)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// uiFinal returns the last frame, or an error when the engine refused or failed the request.
func uiFinal(frames []engine.Reply) (engine.Reply, error) {
	last := frames[len(frames)-1]
	if last.Status == engine.StatusRefused || last.Status == engine.StatusError {
		return last, errors.New("the engine answered " + last.Status + ": " + last.Detail)
	}
	return last, nil
}

// collect reads status, each run's figures and each run's recorded events from the engine; a failed read shows as Error.
func (s *uiServer) collect(ctx context.Context) uiState {
	st := uiState{Runs: []uiRun{}, Queued: []QueuedRow{}, Blocked: []BlockedRow{}, Events: []uiEvent{}}
	d, err := s.readStatus(ctx, "")
	if err != nil {
		st.Error = trimDetail(err)
		return st
	}
	st.Queued, st.Blocked = d.Queued, d.Blocked
	var feed []uiEvent
	for _, r := range d.Runs {
		run := uiRun{RunID: r.RunID, Target: r.Target, Groups: make([]uiGroup, 0, len(r.Groups))}
		for _, g := range r.Groups {
			run.Groups = append(run.Groups, withBlock(uiGroup{Group: g.Group, Stage: g.Stage, State: g.State}, d.Blocked, r.RunID))
		}
		if fd, err := s.readStatus(ctx, r.RunID); err == nil {
			run.Figures = fd.Figures
		}
		feed = append(feed, s.readEvents(ctx, r.RunID)...)
		st.Runs = append(st.Runs, run)
	}
	st.Totals = totalsOf(st)
	slices.SortStableFunc(feed, func(a, b uiEvent) int { return b.Time.Compare(a.Time) })
	st.Events = append(st.Events, feed[:min(len(feed), uiFeedTotal)]...)
	return st
}

// readStatus asks for the status of the target, or of everything shown when it is empty.
func (s *uiServer) readStatus(ctx context.Context, target string) (StatusData, error) {
	req := engine.Request{ID: newID(), Verb: engine.VerbStatus, Target: target, Root: s.deps.Root}
	frames, err := uiAsk(ctx, s.deps.Engine, req)
	if err != nil {
		return StatusData{}, err
	}
	last, err := uiFinal(frames)
	if err != nil {
		return StatusData{}, err
	}
	var d StatusData
	if len(last.Data) > 0 {
		if err := json.Unmarshal(last.Data, &d); err != nil {
			return StatusData{}, errors.New("decode the status: " + err.Error())
		}
	}
	if d.Queued == nil {
		d.Queued = []QueuedRow{}
	}
	if d.Blocked == nil {
		d.Blocked = []BlockedRow{}
	}
	return d, nil
}

// readEvents returns the newest recorded events of a run; a failed read gives none.
func (s *uiServer) readEvents(ctx context.Context, runID string) []uiEvent {
	frames, err := uiAsk(ctx, s.deps.Engine, engine.Request{ID: newID(), Verb: engine.VerbReplay, Target: runID, Root: s.deps.Root})
	if err != nil {
		return nil
	}
	var out []uiEvent
	for _, f := range frames {
		if f.Status != engine.StatusEvent || f.Event == nil {
			continue
		}
		e := f.Event
		out = append(out, uiEvent{Time: e.Time, Type: string(e.Type), Subtype: string(e.Subtype), Run: string(e.Run), Group: string(e.Group)})
	}
	if len(out) > uiFeedPerRun {
		out = out[len(out)-uiFeedPerRun:]
	}
	return out
}

// withBlock adds the reason and question of the blocked row that names this group and run.
func withBlock(g uiGroup, blocked []BlockedRow, runID string) uiGroup {
	for _, b := range blocked {
		if b.Group == g.Group && b.RunID == runID {
			g.Reason, g.Question = b.Reason, b.Question
		}
	}
	return g
}

// totalsOf counts groups by state and sums tokens and metered cost over the runs.
func totalsOf(st uiState) uiTotals {
	t := uiTotals{Queued: len(st.Queued)}
	for _, r := range st.Runs {
		for _, g := range r.Groups {
			switch g.State {
			case "running":
				t.Running++
			case "paused":
				t.Paused++
			case "blocked":
				t.Blocked++
			case "done":
				t.Done++
			default:
				t.Waiting++
			}
		}
		if f := r.Figures; f != nil {
			t.Tokens += f.TokensIn + f.TokensOut
			t.USD += f.USD
		}
	}
	return t
}
