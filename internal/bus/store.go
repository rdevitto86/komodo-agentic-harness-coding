package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// syncFile forces a file's content to disk; tests replace it to count the calls.
var syncFile = func(f *os.File) error { return f.Sync() }

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

// eventLog is an append-only log file; each line goes out in one write with no buffer between.
type eventLog struct {
	path string
	f    *os.File
}

// openLog opens the file at path for appending, creating its folder and the file.
func openLog(path string) (*eventLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("create folder of %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	l := &eventLog{path: path, f: f}
	if endsMidLine(f) {
		// A dead program left a half-written line; end it so the next line starts clean.
		if _, err := f.Write([]byte{'\n'}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("end torn line of %s: %w", path, err)
		}
	}
	return l, nil
}

// endsMidLine reports whether a non-empty file's last byte is not a newline.
func endsMidLine(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return false
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], st.Size()-1); err != nil {
		return false
	}
	return b[0] != '\n'
}

// append writes line and a newline with one write call, then forces it to disk when durable is set.
func (l *eventLog) append(line []byte, durable bool) error {
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	if _, err := l.f.Write(buf); err != nil {
		return fmt.Errorf("append to %s: %w", l.path, err)
	}
	if durable {
		if err := syncFile(l.f); err != nil {
			return fmt.Errorf("sync %s: %w", l.path, err)
		}
	}
	return nil
}

// close closes the file; closing twice is harmless.
func (l *eventLog) close() error {
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("close %s: %w", l.path, err)
	}
	return nil
}

// readRuns reads a runs.json; a missing file reads as an empty map.
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
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, nil
}

// storeLocked appends e to its log before the fan-out; it returns the bus.store_failed events to broadcast after e.
func (b *memBus) storeLocked(e Event) []Event {
	if b.dir == "" {
		return nil
	}
	var out []Event
	fail := func(path string, lost bool, err error) {
		if lost {
			b.missed[path]++
		}
		if !b.failed[path] {
			b.failed[path] = true
			out = append(out, Event{
				Type: TypeError, Subtype: busStoreFailedKey.Subtype, Run: e.Run, Time: e.Time,
				Data: &BusStoreFailed{Run: e.Run, Path: path, Reason: err.Error()},
			})
		}
	}
	path, err := b.placeLocked(e, func(p string, err error) { fail(p, false, err) })
	if err != nil {
		fail(path, true, err)
		return out
	}
	line, err := json.Marshal(e)
	if err != nil {
		fail(path, true, fmt.Errorf("encode event for %s: %w", path, err))
		return out
	}
	durable := e.Type == TypeRunStatus && e.Subtype == "stage.enter" || isFinalError(e, line)
	if err := b.putLocked(path, line, durable); err != nil {
		fail(path, true, err)
		return out
	}
	delete(b.failed, path)
	if e.Type == TypeRunComplete {
		b.closeLogLocked(path)
	}
	return out
}

// placeLocked returns the log path of e; a run-start binds its run to the events folder in its git directory first.
// A failure to save runs.json does not stop the event and goes to soft.
func (b *memBus) placeLocked(e Event, soft func(path string, err error)) (string, error) {
	if e.Run == "" {
		return filepath.Join(b.dir, machineLogName), nil
	}
	if !safeName(e.Run) {
		return "", fmt.Errorf("run id %q cannot name a log file", e.Run)
	}
	if d, ok := e.Data.(*RunStart); ok && e.Type == TypeRunStart {
		if d.CommonDir == "" {
			return "", fmt.Errorf("the run-start of run %s names no git directory", e.Run)
		}
		dir := EventsDir(filepath.Clean(d.CommonDir))
		if b.runDir[e.Run] != dir {
			b.runDir[e.Run] = dir
			if err := b.saveRunsLocked(); err != nil {
				soft(filepath.Join(b.dir, runsFile), err)
			}
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
func (b *memBus) putLocked(path string, line []byte, durable bool) error {
	if n := b.missed[path]; n > 0 {
		gap, _ := json.Marshal(gapLine{Gap: true, Time: now().UTC().Truncate(time.Second), Dropped: n})
		if err := b.appendLocked(path, gap, false); err != nil {
			return err
		}
		delete(b.missed, path)
	}
	return b.appendLocked(path, line, durable)
}

// appendLocked masks line and appends it to the open log at path, opening it first; a failed write closes the log.
func (b *memBus) appendLocked(path string, line []byte, durable bool) error {
	l, ok := b.logs[path]
	if !ok {
		var err error
		if l, err = openLog(path); err != nil {
			return err
		}
		b.logs[path] = l
	}
	if b.mask != nil {
		line = []byte(b.mask(string(line)))
	}
	if err := l.append(line, durable); err != nil {
		b.closeLogLocked(path)
		return err
	}
	return nil
}

// closeLogLocked closes the open log at path.
func (b *memBus) closeLogLocked(path string) {
	if l, ok := b.logs[path]; ok {
		_ = l.close()
		delete(b.logs, path)
	}
}

// saveRunsLocked writes the run-to-folder map to runs.json in the machine events folder.
func (b *memBus) saveRunsLocked() error {
	out, err := json.Marshal(b.runDir)
	if err != nil {
		return fmt.Errorf("encode %s: %w", runsFile, err)
	}
	if err := os.MkdirAll(b.dir, dirPerm); err != nil {
		return fmt.Errorf("create folder %s: %w", b.dir, err)
	}
	return core.WriteFileAtomic(filepath.Join(b.dir, runsFile), out, filePerm)
}

// isFinalError reports whether e is an error event whose data carries final: true.
func isFinalError(e Event, line []byte) bool {
	if e.Type != TypeError {
		return false
	}
	var d struct {
		Data struct {
			Final bool `json:"final"`
		} `json:"data"`
	}
	return json.Unmarshal(line, &d) == nil && d.Data.Final
}
