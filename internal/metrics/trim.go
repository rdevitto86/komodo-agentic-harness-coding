package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// defaultRetentionDays is the retention used when Options.RetentionDays is not positive.
const defaultRetentionDays = 30

// sizeBound is the most bytes a repository's metrics folder may hold; the trim brings it to 80% of this.
var sizeBound int64 = 100 << 20

// now returns the current time; tests replace it.
var now = time.Now

// tailBytes is how much of the end of a log is read to find its last line's time.
const tailBytes = 64 << 10

// logFile is a run log with the time of its last line.
type logFile struct {
	path      string
	run       bus.ID
	size      int64
	last      time.Time
	protected bool // a group of the run is open, paused or blocked
}

// trimDir deletes logs and session records past retention, then the oldest past the size bound.
func trimDir(ctx context.Context, dir string, retentionDays int, t time.Time) error {
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	cutoff := t.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	logs, err := listLogs(ctx, dir)
	if err != nil {
		return err
	}
	var kept []logFile
	for _, l := range logs {
		if !l.protected && l.last.Before(cutoff) {
			if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete %s: %w", l.path, err)
			}
			continue
		}
		kept = append(kept, l)
	}
	sessPath := filepath.Join(dir, "sessions.jsonl")
	if err := rewriteSessions(sessPath, func(lines [][]byte) [][]byte { return keepSince(lines, cutoff) }); err != nil {
		return err
	}
	total, err := folderSize(dir)
	if err != nil || total <= sizeBound {
		return err
	}
	target := sizeBound * 8 / 10
	slices.SortFunc(kept, func(a, b logFile) int { return a.last.Compare(b.last) })
	for _, l := range kept {
		if total <= target {
			return nil
		}
		if l.protected {
			continue
		}
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", l.path, err)
		}
		total -= l.size
	}
	if total <= target {
		return nil
	}
	return rewriteSessions(sessPath, func(lines [][]byte) [][]byte { return dropOldest(lines, total-target) })
}

// listLogs returns the run logs of a folder with their last-line times and protection.
func listLogs(ctx context.Context, dir string) ([]logFile, error) {
	files, err := logFiles(dir)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	groups, _, err := readFigures(ctx, dir)
	if err != nil {
		return nil, err
	}
	open := map[bus.ID]bool{}
	for _, g := range groups {
		if g.Outcome != outcomeDone {
			open[g.Run] = true
		}
	}
	out := make([]logFile, 0, len(files))
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		run, _ := runOfLog(filepath.Base(p))
		last, ok := lastLineTime(p, st.Size())
		if !ok {
			last = st.ModTime()
		}
		out = append(out, logFile{path: p, run: run, size: st.Size(), last: last, protected: open[run]})
	}
	return out, nil
}

// lastLineTime returns the time of the last complete line of a log that parses.
func lastLineTime(path string, size int64) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = f.Close() }()
	start := max(size-tailBytes, 0)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return time.Time{}, false
	}
	// Bytes after the last newline are a torn line.
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i]
	} else {
		return time.Time{}, false
	}
	parts := bytes.Split(buf, []byte{'\n'})
	for i := len(parts) - 1; i >= 0; i-- {
		var l struct {
			Time time.Time `json:"time"`
		}
		if json.Unmarshal(parts[i], &l) == nil && !l.Time.IsZero() {
			return l.Time, true
		}
	}
	return time.Time{}, false
}

// folderSize returns the bytes held by the files of a folder.
func folderSize(dir string) (int64, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int64
	for _, e := range ents {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
	}
	return n, nil
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

// keepSince keeps the session records that started at or after cutoff.
func keepSince(lines [][]byte, cutoff time.Time) [][]byte {
	var out [][]byte
	for _, l := range lines {
		var r SessionRecord
		if json.Unmarshal(l, &r) == nil && !r.StartedAt.Before(cutoff) {
			out = append(out, l)
		}
	}
	return out
}

// dropOldest drops lines from the front until at least need bytes are gone.
func dropOldest(lines [][]byte, need int64) [][]byte {
	var gone int64
	for i, l := range lines {
		if gone >= need {
			return lines[i:]
		}
		gone += int64(len(l)) + 1
	}
	return nil
}
