package bus

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
)

// defaultRetentionDays is the retention used when TrimOptions.RetentionDays is not positive.
const defaultRetentionDays = 30

// sizeBound is the most bytes a repository's events folder may hold; the trim brings it to 80% of this.
var sizeBound int64 = 100 << 20

// tailBytes is how much of the end of a log is read to find its last line's time.
const tailBytes = 64 << 10

// TrimOptions holds what a trim needs beyond the folder.
type TrimOptions struct {
	RetentionDays int           // days a log is kept; a value below 1 means 30
	Open          func(ID) bool // reports whether a run is open; nil means none is
}

// TrimResult says what a trim did.
type TrimResult struct {
	Deleted        []ID  // runs whose log the trim deleted
	ProtectedBytes int64 // bytes held by logs of open runs
	OverBound      bool  // those logs alone exceed the size bound
}

// logFile is a run log with the time of its last line.
type logFile struct {
	path      string
	run       ID
	size      int64
	last      time.Time
	protected bool // the run is open
}

// Trim deletes the logs of an events folder past retention, then the oldest logs past the size bound; the log of an open run stays.
func Trim(ctx context.Context, dir string, o TrimOptions) (TrimResult, error) {
	var res TrimResult
	if o.RetentionDays <= 0 {
		o.RetentionDays = defaultRetentionDays
	}
	cutoff := now().Add(-time.Duration(o.RetentionDays) * 24 * time.Hour)
	logs, err := listLogs(dir, o.Open)
	if err != nil {
		return res, err
	}
	var kept []logFile
	for _, l := range logs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if !l.protected && l.last.Before(cutoff) {
			if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
				return res, fmt.Errorf("delete %s: %w", l.path, err)
			}
			res.Deleted = append(res.Deleted, l.run)
			continue
		}
		if l.protected {
			res.ProtectedBytes += l.size
		}
		kept = append(kept, l)
	}
	res.OverBound = res.ProtectedBytes > sizeBound
	total, err := folderSize(dir)
	if err != nil || total <= sizeBound {
		return res, err
	}
	target := sizeBound * 8 / 10
	slices.SortFunc(kept, func(a, b logFile) int { return a.last.Compare(b.last) })
	for _, l := range kept {
		if total <= target {
			break
		}
		if l.protected {
			continue
		}
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return res, fmt.Errorf("delete %s: %w", l.path, err)
		}
		res.Deleted = append(res.Deleted, l.run)
		total -= l.size
	}
	return res, nil
}

// listLogs returns the run logs of a folder with their last-line times.
func listLogs(dir string, open func(ID) bool) ([]logFile, error) {
	files, err := logFiles(dir)
	if err != nil {
		return nil, err
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
		out = append(out, logFile{path: p, run: run, size: st.Size(), last: last, protected: open != nil && open(run)})
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

// startTrimLocked marks dir for a trim unless it was trimmed within the last day; the caller holds the lock.
func (b *memBus) startTrimLocked(dir string) {
	if b.closed || now().Sub(b.lastTrim[dir]) < trimInterval {
		return
	}
	b.lastTrim[dir] = now()
	b.due = append(b.due, dir)
}

// runDue starts the trims marked during a publish, after the bus lock is released.
func (b *memBus) runDue() {
	b.mu.Lock()
	due := b.due
	b.due = nil
	if b.closed {
		due = nil
	}
	b.trims.Add(len(due))
	b.mu.Unlock()
	for _, dir := range due {
		if b.goFn != nil {
			b.goFn(func() { b.trim(dir) })
		} else {
			b.trim(dir)
		}
	}
}

// trim runs Trim on dir, forgets the runs it deleted, and publishes bus.store_failed for a failed or over-bound trim.
func (b *memBus) trim(dir string) {
	defer b.trims.Done()
	res, err := Trim(context.Background(), dir, TrimOptions{RetentionDays: b.retention, Open: b.runOpen})
	if len(res.Deleted) > 0 {
		b.forget(dir, res.Deleted)
	}
	if err != nil {
		b.reportStore(dir, fmt.Sprintf("trim %s: %v", dir, err))
	}
	if res.OverBound {
		b.reportStore(dir, fmt.Sprintf("the logs of open runs in %s are over the size bound", dir))
	}
}

// forget drops deleted runs of dir from the run-to-folder map and rewrites runs.json.
func (b *memBus) forget(dir string, runs []ID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range runs {
		if b.runDir[r] == dir {
			delete(b.runDir, r)
			b.closeLogLocked(filepath.Join(dir, LogName(r)))
		}
	}
	// A runs.json that cannot be written keeps its stale rows until the next run-start rewrites it.
	_ = b.saveRunsLocked()
}

// reportStore publishes a bus.store_failed event for a problem in the store that belongs to no run; a closed or sealed bus drops it.
func (b *memBus) reportStore(path, reason string) {
	_ = b.Publish(Event{Type: TypeError, Subtype: busStoreFailedKey.Subtype, Data: &BusStoreFailed{Path: path, Reason: reason}})
}
