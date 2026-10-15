package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// Reader is what the CLI reads; a read never writes.
type Reader interface {
	// Runs returns the figures of every run.
	Runs(ctx context.Context) ([]RunMetrics, error)
	// Groups returns the figures of every group of a run.
	Groups(ctx context.Context, run bus.ID) ([]GroupMetrics, error)
	// Explain returns the last n event copies of a run, with their reasons.
	Explain(ctx context.Context, run bus.ID, n int) ([]bus.Event, error)
	// Replay returns a run's events in order, or one group's when group is not zero; it drops a torn last line and marks the gap.
	Replay(ctx context.Context, run, group bus.ID) (ReplayLog, error)
	// Scorecard returns one row per agent and model over the window.
	Scorecard(ctx context.Context, window time.Duration) (Scorecard, error)
}

// ReplayLog is a run's event copies in the order written, with every gap marked.
type ReplayLog struct {
	Events  []bus.Event `json:"events"`
	Gaps    []Gap       `json:"gaps"`
	Skipped int         `json:"skipped"` // lines before the last that did not parse
}

// Gap marks where a run's log is missing events.
type Gap struct {
	After   int       `json:"after"`             // how many events came before the gap
	Kind    string    `json:"kind"`              // dropped (the bus dropped events) or torn (the last line was half-written)
	Dropped int       `json:"dropped,omitempty"` // events lost, for kind dropped
	Time    time.Time `json:"time,omitzero"`     // from the gap line; zero for torn
}

// The kinds of gap.
const (
	gapDropped = "dropped"
	gapTorn    = "torn"
)

// gapLine is the line that records events the bus dropped before the Service read them.
type gapLine struct {
	Gap     bool      `json:"gap"`
	Time    time.Time `json:"time"`
	Dropped int       `json:"dropped"`
}

// fileReader is the file-backed Reader over one repository's metrics folder.
type fileReader struct{ dir string }

// NewReader returns a Reader over the metrics folder of one repository.
func NewReader(dir string) Reader { return &fileReader{dir: dir} }

// Runs returns the figures of every run, oldest first.
func (r *fileReader) Runs(ctx context.Context) ([]RunMetrics, error) {
	_, runs, err := readFigures(ctx, r.dir)
	if err != nil {
		return nil, err
	}
	out := make([]RunMetrics, 0, len(runs))
	for _, m := range runs {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b RunMetrics) int { return strings.Compare(string(a.Run), string(b.Run)) })
	return out, nil
}

// Groups returns the figures of every group of a run, oldest first.
func (r *fileReader) Groups(ctx context.Context, run bus.ID) ([]GroupMetrics, error) {
	groups, _, err := readFigures(ctx, r.dir)
	if err != nil {
		return nil, err
	}
	var out []GroupMetrics
	for _, g := range groups[run] {
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b GroupMetrics) int { return strings.Compare(string(a.Group), string(b.Group)) })
	return out, nil
}

// Explain returns the last n event copies of a run in the order written.
func (r *fileReader) Explain(ctx context.Context, run bus.ID, n int) ([]bus.Event, error) {
	log, err := r.Replay(ctx, run, "")
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	return log.Events[max(len(log.Events)-n, 0):], nil
}

// Replay returns a run's events, or one group's, with every gap marked.
func (r *fileReader) Replay(ctx context.Context, run, group bus.ID) (ReplayLog, error) {
	if err := ctx.Err(); err != nil {
		return ReplayLog{}, err
	}
	if !safeName(run) {
		return ReplayLog{}, fmt.Errorf("run %q is not a run id", run)
	}
	log, err := readLog(filepath.Join(r.dir, LogName(run)), group)
	if errors.Is(err, os.ErrNotExist) {
		return ReplayLog{}, fmt.Errorf("no event log for run %s", run)
	}
	if err != nil {
		return ReplayLog{}, fmt.Errorf("read event log of run %s: %w", run, err)
	}
	return log, nil
}

// Scorecard returns one row per agent and model over the window.
func (r *fileReader) Scorecard(ctx context.Context, window time.Duration) (Scorecard, error) {
	return scorecard(ctx, r.dir, window, now())
}

// readLog reads one event log; a group other than zero keeps only that group's events and every gap.
func readLog(path string, group bus.ID) (ReplayLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return ReplayLog{}, err
	}
	defer func() { _ = f.Close() }()
	out := ReplayLog{Events: []bus.Event{}, Gaps: []Gap{}}
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return ReplayLog{}, err
		}
		if errors.Is(err, io.EOF) {
			if len(strings.TrimSpace(string(line))) > 0 {
				out.Gaps = append(out.Gaps, Gap{After: len(out.Events), Kind: gapTorn})
			}
			return out, nil
		}
		out.addLine(line, group)
	}
}

// addLine files one complete line as an event, a gap mark or a skipped line.
func (l *ReplayLog) addLine(line []byte, group bus.ID) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return
	}
	var g gapLine
	if json.Unmarshal(line, &g) == nil && g.Gap {
		l.Gaps = append(l.Gaps, Gap{After: len(l.Events), Kind: gapDropped, Dropped: g.Dropped, Time: g.Time})
		return
	}
	var e bus.Event
	if err := json.Unmarshal(line, &e); err != nil {
		l.Skipped++
		return
	}
	if group == "" || e.Group == group {
		l.Events = append(l.Events, e)
	}
}

// logFiles returns the run logs of a folder, by file name.
func logFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if _, ok := runOfLog(e.Name()); ok && !e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// rebuildFigures recomputes the figures of a folder from its run logs.
func rebuildFigures(ctx context.Context, dir string) (*figures, error) {
	files, err := logFiles(dir)
	if err != nil {
		return nil, err
	}
	f := newFigures()
	f.dir = dir
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log, err := readLog(p, "")
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		for _, e := range log.Events {
			f.apply(e)
		}
	}
	return f, nil
}

// readFigures reads groups.json and runs.json; a missing file reads as empty and an unreadable one is rebuilt in memory from the run logs.
func readFigures(ctx context.Context, dir string) (map[bus.ID]map[bus.ID]GroupMetrics, map[bus.ID]RunMetrics, error) {
	groups, runs := map[bus.ID]map[bus.ID]GroupMetrics{}, map[bus.ID]RunMetrics{}
	e1 := readJSON(filepath.Join(dir, "groups.json"), &groups)
	e2 := readJSON(filepath.Join(dir, "runs.json"), &runs)
	if e1 == nil && e2 == nil {
		return groups, runs, nil
	}
	f, err := rebuildFigures(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	g, r := f.snapshot()
	return g, r, nil
}

// readJSON decodes a file into v; a missing file leaves v empty and returns nil.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// readSessions reads sessions.jsonl, skipping lines that do not parse.
func readSessions(path string) ([]SessionRecord, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []SessionRecord
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var r SessionRecord
			if json.Unmarshal(line, &r) == nil {
				out = append(out, r)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
	}
}
