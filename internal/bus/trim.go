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
	Open          func(ID) bool // reports whether a run is open; nil counts every run as open, so nothing is deleted
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
	protected bool // the run is open, or the log was written to within the last day
}

// Trim deletes the logs of an events folder past retention, then the oldest logs past the size bound.
// A log stays when its run is open or when it was written to within the last day.
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

// recentWrite is how recently a log must have been written to for the trim to spare it.
const recentWrite = 24 * time.Hour

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
		out = append(out, logFile{path: p, run: run, size: st.Size(), last: last, protected: open == nil || open(run) || now().Sub(st.ModTime()) < recentWrite})
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

// dueLocked reports whether the trim named by key is due, and stamps it as started if so.
func (b *memBus) dueLocked(key string) bool {
	if now().Sub(b.lastTrim[key]) < trimInterval {
		return false
	}
	b.lastTrim[key] = now()
	return true
}

// startTrimLocked marks dir, and the machine log, for a trim unless each was trimmed within the last day; the caller holds the lock.
func (b *memBus) startTrimLocked(dir string) {
	if b.closed {
		return
	}
	if b.dueLocked(dir) {
		b.follow.trims = append(b.follow.trims, trimJob{dir: dir})
	}
	if b.dueLocked("\x00machine") {
		b.follow.trims = append(b.follow.trims, trimJob{dir: b.dir, machine: true})
	}
}

// startTrims starts the trims a publish marked, after the bus lock is released.
func (b *memBus) startTrims(jobs []trimJob) {
	if len(jobs) == 0 {
		return
	}
	b.mu.Lock()
	if b.closed {
		jobs = nil
	}
	b.trims.Add(len(jobs))
	b.mu.Unlock()
	for _, j := range jobs {
		if b.goFn != nil {
			b.goFn(func() { b.trim(j) })
		} else {
			b.trim(j)
		}
	}
}

// trim runs one job, forgets the runs it deleted, and publishes bus.store_failed for a failed or over-bound trim.
func (b *memBus) trim(j trimJob) {
	defer b.trims.Done()
	if j.machine {
		if err := b.trimMachineLog(); err != nil {
			b.reportStore("", filepath.Join(j.dir, machineLogName), fmt.Sprintf("trim: %v", err))
		}
		return
	}
	res, err := Trim(context.Background(), j.dir, TrimOptions{RetentionDays: b.retention, Open: b.runOpen})
	if len(res.Deleted) > 0 {
		b.forget(j.dir, res.Deleted)
	}
	if err != nil {
		b.reportStore("", j.dir, fmt.Sprintf("trim %s: %v", j.dir, err))
	}
	if res.OverBound {
		b.reportStore("", j.dir, fmt.Sprintf("the logs of open runs in %s are over the size bound", j.dir))
	}
}

// forget drops deleted runs of dir from the run-to-folder map and from runs.json.
func (b *memBus) forget(dir string, runs []ID) {
	b.mu.Lock()
	var gone []ID
	for _, r := range runs {
		if b.runDir[r] == dir {
			delete(b.runDir, r)
			b.closeLogLocked(filepath.Join(dir, LogName(r)))
			gone = append(gone, r)
		}
	}
	b.mu.Unlock()
	b.saveRuns(nil, gone)
}

// trimMachineLog rewrites engine.jsonl keeping the lines within retention and under the size bound, oldest lines dropped first.
// It reads a snapshot outside the lock, then under the lock adds the lines appended since and swaps the file in.
func (b *memBus) trimMachineLog() error {
	path := filepath.Join(b.dir, machineLogName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Bytes after the last newline are a line still being written; the swap carries them over.
	end := bytes.LastIndexByte(data, '\n') + 1
	lines := bytes.SplitAfter(data[:end], []byte{'\n'})
	lines = lines[:len(lines)-1]
	days := b.retention
	if days <= 0 {
		days = defaultRetentionDays
	}
	cutoff := now().Add(-time.Duration(days) * 24 * time.Hour)
	var kept [][]byte
	var total int64
	for _, l := range lines {
		var t struct {
			Time time.Time `json:"time"`
		}
		if json.Unmarshal(l, &t) == nil && !t.Time.IsZero() && t.Time.Before(cutoff) {
			continue
		}
		kept = append(kept, l)
		total += int64(len(l))
	}
	if total > sizeBound {
		for total > sizeBound*8/10 && len(kept) > 0 {
			total -= int64(len(kept[0]))
			kept = kept[1:]
		}
	}
	if len(kept) == len(lines) {
		return nil
	}
	tmp := path + ".trim"
	if err := os.WriteFile(tmp, bytes.Join(kept, nil), filePerm); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.swapMachineLogLocked(path, tmp, int64(end)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// swapMachineLogLocked appends what engine.jsonl gained after offset to tmp, closes the log and renames tmp over it.
func (b *memBus) swapMachineLogLocked(path, tmp string, offset int64) error {
	b.closeLogLocked(path)
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	dst, err := os.OpenFile(tmp, os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
