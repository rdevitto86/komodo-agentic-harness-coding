package bus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reader is what replay and the metrics rebuild read; a read never writes.
type Reader interface {
	// Replay returns a run's events in order, or one group's when group is not empty; it drops a torn last line and marks the gap.
	Replay(ctx context.Context, run, group ID) (ReplayLog, error)
	// Explain returns the last n event copies of a run.
	Explain(ctx context.Context, run ID, n int) ([]Event, error)
}

// ReplayLog is a run's event copies in the order written, with every gap marked.
type ReplayLog struct {
	Events  []Event `json:"events"`
	Gaps    []Gap   `json:"gaps"`
	Skipped int     `json:"skipped"` // lines before the last that did not parse
}

// Gap marks where a run's log is missing events.
type Gap struct {
	After   int       `json:"after"`             // how many events came before the gap
	Kind    string    `json:"kind"`              // dropped (the store lost events) or torn (the last line was half-written)
	Dropped int       `json:"dropped,omitempty"` // events lost, for kind dropped
	Time    time.Time `json:"time,omitzero"`     // from the gap line; zero for torn
}

// The kinds of gap.
const (
	gapDropped = "dropped"
	gapTorn    = "torn"
)

// fileReader is the file-backed Reader over the machine events folder.
type fileReader struct{ dir string }

// NewReader returns a Reader that finds each run's folder in the runs.json of the machine events folder dir.
func NewReader(dir string) Reader { return &fileReader{dir: dir} }

// Replay returns a run's events, or one group's, with every gap marked.
func (r *fileReader) Replay(ctx context.Context, run, group ID) (ReplayLog, error) {
	if err := ctx.Err(); err != nil {
		return ReplayLog{}, err
	}
	if !safeName(run) {
		return ReplayLog{}, fmt.Errorf("run %q is not a run id", run)
	}
	runs, err := readRuns(filepath.Join(r.dir, runsFile))
	if err != nil {
		return ReplayLog{}, fmt.Errorf("read run folders: %w", err)
	}
	dir, ok := runs[run]
	if !ok {
		return ReplayLog{}, fmt.Errorf("no event log for run %s", run)
	}
	log, err := readLog(filepath.Join(dir, LogName(run)), group)
	if errors.Is(err, os.ErrNotExist) {
		return ReplayLog{}, fmt.Errorf("no event log for run %s", run)
	}
	if err != nil {
		return ReplayLog{}, fmt.Errorf("read event log of run %s: %w", run, err)
	}
	return log, nil
}

// Explain returns the last n event copies of a run in the order written.
func (r *fileReader) Explain(ctx context.Context, run ID, n int) ([]Event, error) {
	log, err := r.Replay(ctx, run, "")
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	return log.Events[max(len(log.Events)-n, 0):], nil
}

// ReadFolder calls fn with each run log of an events folder in file-name order; a missing folder holds none.
func ReadFolder(ctx context.Context, dir string, fn func(run ID, l ReplayLog) error) error {
	files, err := logFiles(dir)
	if err != nil {
		return err
	}
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		log, err := readLog(p, "")
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		run, _ := runOfLog(filepath.Base(p))
		if err := fn(run, log); err != nil {
			return err
		}
	}
	return nil
}

// readLog reads one event log; a group other than empty keeps only that group's events and every gap.
func readLog(path string, group ID) (ReplayLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return ReplayLog{}, err
	}
	defer func() { _ = f.Close() }()
	out := ReplayLog{Events: []Event{}, Gaps: []Gap{}}
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
func (l *ReplayLog) addLine(line []byte, group ID) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return
	}
	var g gapLine
	if json.Unmarshal(line, &g) == nil && g.Gap {
		l.Gaps = append(l.Gaps, Gap{After: len(l.Events), Kind: gapDropped, Dropped: g.Dropped, Time: g.Time})
		return
	}
	var e Event
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
