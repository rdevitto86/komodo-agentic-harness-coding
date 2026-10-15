package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// defaultRetentionDays is the retention used when Options.RetentionDays is not positive.
const defaultRetentionDays = 30

// now returns the current time; tests replace it.
var now = time.Now

// trimResult says what a trim did.
type trimResult struct {
	Deleted []bus.ID // runs whose figures the trim dropped
}

// trimDir drops session records past retention and names the runs that ended before the cutoff, so the caller drops their figures.
func trimDir(ctx context.Context, dir string, retentionDays int, t time.Time) (trimResult, error) {
	var res trimResult
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	cutoff := t.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	_, runs, err := readFigures(ctx, dir)
	if err != nil {
		return res, err
	}
	for id, r := range runs {
		if !r.EndedAt.IsZero() && r.EndedAt.Before(cutoff) {
			res.Deleted = append(res.Deleted, id)
		}
	}
	slices.Sort(res.Deleted)
	err = rewriteSessions(filepath.Join(dir, "sessions.jsonl"), func(lines [][]byte) [][]byte { return keepSince(lines, cutoff) })
	return res, err
}

// rewriteSessions replaces sessions.jsonl with the lines edit keeps; the file is untouched when edit drops nothing.
func rewriteSessions(path string, edit func(lines [][]byte) [][]byte) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var lines [][]byte
	for _, l := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	kept := edit(lines)
	if len(kept) == len(lines) {
		return nil
	}
	var out []byte
	for _, l := range kept {
		out = append(append(out, l...), '\n')
	}
	return core.WriteFileAtomic(path, out, filePerm)
}

// keepSince keeps the session records that ended at or after cutoff; a record with no time and a line that does not parse stay.
func keepSince(lines [][]byte, cutoff time.Time) [][]byte {
	var out [][]byte
	for _, l := range lines {
		var r SessionRecord
		if json.Unmarshal(l, &r) != nil || r.age().IsZero() || !r.age().Before(cutoff) {
			out = append(out, l)
		}
	}
	return out
}
