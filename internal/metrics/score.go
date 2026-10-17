package metrics

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// ScoreRow is one agent-and-model line of the scorecard.
type ScoreRow struct {
	Agent            string  `json:"agent"`
	Model            string  `json:"model"`
	FirstPassRate    float64 `json:"first_pass_rate"`   // share of tasks done on the first attempt
	ReturnsPerGroup  float64 `json:"returns_per_group"` // returns to Build per group
	FindingPrecision float64 `json:"finding_precision"` // share of findings Go verified
	// share of tester failures the reruns passed
	TesterFalseFail float64  `json:"tester_false_fail"`
	CacheReadShare  float64  `json:"cache_read_share"` // cache-read tokens over input tokens
	TokensPerGroup  *int64   `json:"tokens_per_group"` // nil when no usage was reported
	USDPerGroup     *float64 `json:"usd_per_group"`    // nil when no usage was reported
}

// Scorecard is the whole report: one row per agent and model.
type Scorecard struct {
	Rows []ScoreRow `json:"rows"`
}

// groupKey names one group of one run.
type groupKey struct{ run, group bus.ID }

// rowKey names one agent and model.
type rowKey struct{ agent, model string }

// tally holds the counts one row is worked out from.
type tally struct {
	tasksDone, firstPass int
	returns              int
	groups               map[groupKey]bool // groups the row held a session in
	submitted, verified  int
	failed, disputed     int
	tokensIn, cacheRead  int
	groupTokens          map[groupKey]int64   // tokens per group that reached Done
	groupUSD             map[groupKey]float64 // cost per group that reached Done
}

// row turns a tally into a scorecard row.
func (tl *tally) row(k rowKey) ScoreRow {
	r := ScoreRow{
		Agent: k.agent, Model: k.model,
		FirstPassRate:    ratio(tl.firstPass, tl.tasksDone),
		ReturnsPerGroup:  ratio(tl.returns, len(tl.groups)),
		FindingPrecision: ratio(tl.verified, tl.submitted),
		TesterFalseFail:  ratio(tl.disputed, tl.failed+tl.disputed),
		CacheReadShare:   ratio(tl.cacheRead, tl.tokensIn),
	}
	if n := len(tl.groupTokens); n > 0 {
		var (
			tok int64
			usd float64
		)

		for g, v := range tl.groupTokens {
			tok += v
			usd += tl.groupUSD[g]
		}

		perTok, perUSD := tok/int64(n), usd/float64(n)
		r.TokensPerGroup, r.USDPerGroup = &perTok, &perUSD
	}

	return r
}

// scorecard works out the scorecard of a folder from the session records and the bus store's run
// logs inside the window.
func scorecard(
	ctx context.Context,
	dir string,
	window time.Duration,
	t time.Time,
) (Scorecard, error) {
	var cutoff time.Time
	if window > 0 {
		cutoff = t.Add(-window)
	}

	recs, err := readSessions(filepath.Join(dir, "sessions.jsonl"))
	if err != nil {
		return Scorecard{}, fmt.Errorf("read sessions: %w", err)
	}

	byID := map[bus.ID]SessionRecord{}
	rows := map[rowKey]*tally{}

	for _, r := range recs {
		if !r.age().IsZero() && r.age().Before(cutoff) {
			continue
		}

		byID[r.Session] = r
		tl := tallyOf(rows, rowKey{r.Agent, r.Model})

		tl.groups[groupKey{r.Run, r.Group}] = true
		if r.Usage != nil {
			tl.tokensIn += r.Usage.TokensIn
			tl.cacheRead += r.Usage.CacheRead
		}
	}

	doneGroups := map[groupKey]bool{}

	err = bus.ReadFolder(ctx, eventsDirOf(dir), func(_ bus.ID, l bus.ReplayLog) error {
		countEvents(l.Events, cutoff, byID, rows, doneGroups)

		return nil
	})
	if err != nil {
		return Scorecard{}, err
	}

	for _, r := range byID {
		gk := groupKey{r.Run, r.Group}
		if r.Usage != nil && doneGroups[gk] {
			tl := rows[rowKey{r.Agent, r.Model}]
			tl.groupTokens[gk] += int64(r.Usage.TokensIn + r.Usage.TokensOut)
			tl.groupUSD[gk] += r.Usage.USD + r.Usage.USDEquiv
		}
	}

	out := Scorecard{Rows: make([]ScoreRow, 0, len(rows))}
	for k, tl := range rows {
		out.Rows = append(out.Rows, tl.row(k))
	}

	slices.SortFunc(out.Rows, func(a, b ScoreRow) int {
		return strings.Compare(a.Agent+"\x00"+a.Model, b.Agent+"\x00"+b.Model)
	})

	return out, nil
}

// tallyOf returns the tally of a row, creating it.
func tallyOf(rows map[rowKey]*tally, k rowKey) *tally {
	tl, ok := rows[k]
	if !ok {
		tl = &tally{
			groups:      map[groupKey]bool{},
			groupTokens: map[groupKey]int64{},
			groupUSD:    map[groupKey]float64{},
		}
		rows[k] = tl
	}

	return tl
}

// countEvents adds one run log's events to the row tallies; sessions it cannot place are not
// counted.
func countEvents(
	evs []bus.Event,
	cutoff time.Time,
	byID map[bus.ID]SessionRecord,
	rows map[rowKey]*tally,
	doneGroups map[groupKey]bool,
) {
	type taskKey struct {
		group groupKey
		task  string
	}

	retried := map[taskKey]bool{}
	lastBuilder := map[groupKey]rowKey{} // the builder row of each group's latest builder session
	rowOf := func(session bus.ID) (*tally, rowKey, bool) {
		r, ok := byID[session]
		if !ok {
			return nil, rowKey{}, false
		}

		k := rowKey{r.Agent, r.Model}

		return rows[k], k, true
	}

	for _, e := range evs {
		if e.Time.Before(cutoff) {
			continue
		}

		switch d := e.Data.(type) {
		case *bus.TaskRetry:
			retried[taskKey{groupKey{e.Run, e.Group}, d.Task}] = true
		case *bus.TaskDone:
			if tl, _, ok := rowOf(d.Session); ok {
				tl.tasksDone++
				if !retried[taskKey{groupKey{e.Run, e.Group}, d.Task}] {
					tl.firstPass++
				}
			}
		case *bus.SessionEnd:
			tl, k, ok := rowOf(d.Session)
			if !ok {
				continue
			}

			if k.agent == "builder" {
				lastBuilder[groupKey{e.Run, e.Group}] = k
			}

			if k.agent == "tester" && d.Verify != nil {
				tl.failed += len(d.Verify.Failed)
				tl.disputed += len(d.Verify.Disputed)
			}
		case *bus.StageEnter:
			if k, ok := lastBuilder[groupKey{e.Run, e.Group}]; ok && d.Return {
				rows[k].returns++
			}
		case *bus.FindingSubmit:
			if tl, _, ok := rowOf(d.Session); ok {
				tl.submitted++
			}
		case *bus.FindingVerified:
			if tl, _, ok := rowOf(d.Session); ok {
				tl.verified++
			}
		case *bus.RunDone:
			for _, g := range d.Done {
				doneGroups[groupKey{e.Run, g}] = true
			}
		}
	}
}

// ratio returns a over b, or 0 when b is 0.
func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}

	return float64(a) / float64(b)
}
