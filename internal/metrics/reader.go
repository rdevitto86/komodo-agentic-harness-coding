package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	// Scorecard returns one row per agent and model over the window.
	Scorecard(ctx context.Context, window time.Duration) (Scorecard, error)
}

// fileReader is the file-backed Reader over one repository's metrics folder; it rebuilds missing figures from the bus store.
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

// Scorecard returns one row per agent and model over the window.
func (r *fileReader) Scorecard(ctx context.Context, window time.Duration) (Scorecard, error) {
	return scorecard(ctx, r.dir, window, now())
}

// rebuildFigures recomputes the figures of a folder from the run logs in the bus's event store beside it.
func rebuildFigures(ctx context.Context, dir string) (*figures, error) {
	f := newFigures()
	f.dir = dir
	err := bus.ReadFolder(ctx, eventsDirOf(dir), func(_ bus.ID, l bus.ReplayLog) error {
		for _, e := range l.Events {
			f.apply(e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// readFigures reads groups.json and runs.json; a missing file reads as empty and an unreadable one is rebuilt in memory from the bus store.
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
