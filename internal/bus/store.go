package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// machineLogName is the log of events that name no run, kept in the machine events folder.
const machineLogName = "engine.jsonl"

// runsFile is the machine events folder's map from each run to the events folder of its repository.
const runsFile = "runs.json"

// trimInterval is the least time between two background trims of one events folder.
const trimInterval = 24 * time.Hour

// maxOpenLogs is the most log files the bus holds open; the least recently written is closed first.
const maxOpenLogs = 64

// syncFile forces a file's content to disk; tests replace it to count or slow the calls.
var syncFile = func(f *os.File) error { return f.Sync() }

// Finaler is implemented by the data of an error event that may be the last one its process publishes.
type Finaler interface{ IsFinal() bool }

// LogName returns the file name of a run's event log.
func LogName(run ID) string { return string(run) + ".jsonl" }

// runOfLog returns the run id a log file name carries, and whether the name is a run log's.
func runOfLog(name string) (ID, bool) {
	id, ok := strings.CutSuffix(name, ".jsonl")
	return ID(id), ok && id != ""
}

// EventsDir returns a repository's events folder inside its git common directory.
func EventsDir(commonDir string) string { return filepath.Join(commonDir, "komodo", "events") }

// safeName reports whether id can be one path segment of a file name.
func safeName(id ID) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(string(id), `/\`+"\x00")
}

// gapLine is the line that marks events the store could not append to its log.
type gapLine struct {
	Gap     bool      `json:"gap"`
	Time    time.Time `json:"time"`
	Dropped int       `json:"dropped"`
}

// openLog is an open event log with the sequence number of its latest write.
type openLog struct {
	*core.LineFile
	used uint64
}

// syncJob is a log to force to disk once Publish has released the lock.
type syncJob struct {
	path string
	run  ID // the run whose event asked for the sync; empty when another event's final error asked
}

// trimJob is an events folder to trim, or the machine log.
type trimJob struct {
	dir     string
	machine bool
}

// followUp is the work a publish leaves for after it releases the lock.
type followUp struct {
	syncs []syncJob
	trims []trimJob
	adds  map[ID]string // run-to-folder rows to save to runs.json
}

// corruptRunsError marks a runs.json whose content is not a run-to-folder map.
type corruptRunsError struct {
	path string
	err  error
}

// Error names the file and the decoding failure.
func (e *corruptRunsError) Error() string { return fmt.Sprintf("decode %s: %v", e.path, e.err) }

// Unwrap returns the decoding failure.
func (e *corruptRunsError) Unwrap() error { return e.err }

// readRuns reads a runs.json; a missing file reads as an empty map and a corrupt one as a corruptRunsError.
func readRuns(path string) (map[ID]string, error) {
	out := map[ID]string{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, &corruptRunsError{path: path, err: err}
	}
	return out, nil
}

// loadRuns reads runs.json for a writer; a corrupt file is renamed aside and note says where it went.
func loadRuns(path string) (m map[ID]string, note string, err error) {
	m, err = readRuns(path)
	var bad *corruptRunsError
	if !errors.As(err, &bad) {
		return m, "", err
	}
	aside := fmt.Sprintf("%s.bad-%d", path, now().Unix())
	if err := os.Rename(path, aside); err != nil {
		return nil, "", fmt.Errorf("move corrupt %s aside: %w", path, err)
	}
	return map[ID]string{}, fmt.Sprintf("%v; moved to %s", bad, aside), nil
}

// storeLocked appends e to its log before the fan-out; it returns the bus.store_failed events to broadcast after e.
func (b *memBus) storeLocked(e Event) []Event {
	if b.dir == "" {
		return nil
	}
	var out []Event
	// A burst of failures on one key is announced once and ends when the key succeeds again.
	announce := func(key, path string, err error) {
		if !b.failed[key] {
			b.failed[key] = true
			out = append(out, Event{
				Type: TypeError, Subtype: busStoreFailedKey.Subtype, Run: e.Run, Time: e.Time,
				Data: &BusStoreFailed{Run: e.Run, Path: path, Reason: err.Error()},
			})
		}
	}
	unplaced := "unplaced:" + string(e.Run)
	path, err := b.placeLocked(e)
	if err != nil {
		announce(unplaced, "", err)
		return out
	}
	delete(b.failed, unplaced)
	line, err := json.Marshal(e)
	if err != nil {
		b.missed[path]++
		announce(path, path, fmt.Errorf("encode event for %s: %w", path, err))
		return out
	}
	if err := b.putLocked(path, line); err != nil {
		b.missed[path]++
		announce(path, path, err)
		return out
	}
	delete(b.failed, path)
	b.wantSyncLocked(e, path)
	if e.Type == TypeRunComplete || e.Type == TypeRunFailure && e.Subtype == "stalled" {
		b.closeLogLocked(path)
	}
	return out
}

// wantSyncLocked queues the syncs an event asks for: its own log for a stage.enter, every open log for a final error.
func (b *memBus) wantSyncLocked(e Event, path string) {
	add := func(p string, run ID) {
		if !slices.ContainsFunc(b.follow.syncs, func(j syncJob) bool { return j.path == p }) {
			b.follow.syncs = append(b.follow.syncs, syncJob{path: p, run: run})
		}
	}
	switch f, final := e.Data.(Finaler); {
	case e.Type == TypeRunStatus && e.Subtype == "stage.enter":
		add(path, e.Run)
	case e.Type == TypeError && final && f.IsFinal():
		add(path, e.Run)
		for p := range b.logs {
			add(p, "")
		}
	}
}

// RepoBound is implemented by an event payload that names the repository its run belongs to, such as a plan request.
type RepoBound interface {
	// GitCommonDir returns the repository's common folder, or empty when the payload names none.
	GitCommonDir() string
}

// placeLocked returns the log path of e; a run-start binds its run to the events folder in its git directory first.
func (b *memBus) placeLocked(e Event) (string, error) {
	if e.Run == "" {
		return filepath.Join(b.dir, machineLogName), nil
	}
	if !safeName(e.Run) {
		return "", fmt.Errorf("run id %q cannot name a log file", e.Run)
	}
	common, bind := "", false
	switch d := e.Data.(type) {
	case *RunStart:
		if e.Type == TypeRunStart {
			if d.CommonDir == "" {
				return "", fmt.Errorf("the run-start of run %s names no git directory", e.Run)
			}
			common, bind = d.CommonDir, true
		}
	case RepoBound:
		common = d.GitCommonDir()
		bind = common != ""
	}
	if bind {
		dir := EventsDir(filepath.Clean(common))
		if b.runDir[e.Run] != dir {
			b.runDir[e.Run] = dir
			if b.follow.adds == nil {
				b.follow.adds = map[ID]string{}
			}
			b.follow.adds[e.Run] = dir
		}
		b.startTrimLocked(dir)
	}
	dir, ok := b.runDir[e.Run]
	if !ok {
		return "", fmt.Errorf("no repository known for run %s", e.Run)
	}
	return filepath.Join(dir, LogName(e.Run)), nil
}

// putLocked writes line, masked, to the log at path, first a gap line for the events an earlier failure lost.
func (b *memBus) putLocked(path string, line []byte) error {
	if n := b.missed[path]; n > 0 {
		gap, _ := json.Marshal(gapLine{Gap: true, Time: now().UTC().Truncate(time.Second), Dropped: n})
		if err := b.appendLocked(path, gap); err != nil {
			return err
		}
		delete(b.missed, path)
	}
	return b.appendLocked(path, line)
}

// appendLocked masks line and appends it to the log at path, opening it first and closing the least recently written log at the cap.
func (b *memBus) appendLocked(path string, line []byte) error {
	l, ok := b.logs[path]
	if !ok {
		if len(b.logs) >= maxOpenLogs {
			b.evictLocked()
		}
		lf, err := core.OpenLineFile(path)
		if err != nil {
			return err
		}
		l = &openLog{LineFile: lf}
		b.logs[path] = l
	}
	b.useSeq++
	l.used = b.useSeq
	if b.mask != nil {
		line = []byte(b.mask(string(line)))
	}
	if err := l.Append(line); err != nil {
		b.closeLogLocked(path)
		return err
	}
	return nil
}

// evictLocked closes the open log that was written longest ago.
func (b *memBus) evictLocked() {
	var oldest string
	var seq uint64
	for p, l := range b.logs {
		if oldest == "" || l.used < seq {
			oldest, seq = p, l.used
		}
	}
	if oldest != "" {
		b.closeLogLocked(oldest)
	}
}

// closeLogLocked closes the open log at path.
func (b *memBus) closeLogLocked(path string) {
	if l, ok := b.logs[path]; ok {
		_ = l.Close()
		delete(b.logs, path)
	}
}

// finish does the work a publish left for after the lock: syncs, runs.json rows and trims.
func (b *memBus) finish(fu followUp) {
	for _, j := range fu.syncs {
		b.syncLog(j)
	}
	if len(fu.adds) > 0 {
		b.saveRuns(fu.adds, nil)
	}
	b.startTrims(fu.trims)
}

// syncLog forces the file at j.path to disk; a failure is announced once per burst and is not a lost event.
func (b *memBus) syncLog(j syncJob) {
	err := syncPath(j.path)
	key := "sync:" + j.path
	b.mu.Lock()
	announce := err != nil && !b.failed[key]
	if err != nil {
		b.failed[key] = true
	} else {
		delete(b.failed, key)
	}
	b.mu.Unlock()
	if announce {
		b.reportStore(j.run, j.path, "sync: "+err.Error())
	}
}

// syncPath opens the file at path and forces it to disk; one sync covers every write made to the file before it.
func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return syncFile(f)
}

// saveRuns merges the rows into the runs.json on disk and drops the removed runs, outside the bus lock.
// A failure is announced once per burst and the burst ends with the next save that works.
func (b *memBus) saveRuns(adds map[ID]string, removes []ID) {
	path := filepath.Join(b.dir, runsFile)
	note, err := b.mergeRuns(path, adds, removes)
	if note != "" {
		b.reportStore("", path, note)
	}
	b.mu.Lock()
	announce := err != nil && !b.failed[runsFile]
	if err != nil {
		b.failed[runsFile] = true
	} else {
		delete(b.failed, runsFile)
	}
	b.mu.Unlock()
	if announce {
		b.reportStore("", path, err.Error())
	}
}

// mergeRuns reads runs.json again, so rows it did not know survive, applies the change and writes the file.
func (b *memBus) mergeRuns(path string, adds map[ID]string, removes []ID) (note string, err error) {
	b.saveMu.Lock()
	defer b.saveMu.Unlock()
	cur, note, err := loadRuns(path)
	if err != nil {
		return "", err
	}
	maps.Copy(cur, adds)
	for _, r := range removes {
		delete(cur, r)
	}
	out, err := json.Marshal(cur)
	if err != nil {
		return note, fmt.Errorf("encode %s: %w", runsFile, err)
	}
	if err := os.MkdirAll(b.dir, dirPerm); err != nil {
		return note, fmt.Errorf("create folder %s: %w", b.dir, err)
	}
	return note, core.WriteFileAtomic(path, out, filePerm)
}

// reportStore publishes a bus.store_failed event for a problem in the store; a closed or sealed bus drops it.
func (b *memBus) reportStore(run ID, path, reason string) {
	_ = b.Publish(Event{Type: TypeError, Subtype: busStoreFailedKey.Subtype, Run: run, Data: &BusStoreFailed{Run: run, Path: path, Reason: reason}})
}
