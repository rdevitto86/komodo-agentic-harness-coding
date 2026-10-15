package metrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// gitTimeout bounds the git call that finds a repository's git directory.
const gitTimeout = 10 * time.Second

// machineLogName is the log of events that name no run, kept in the machine metrics folder.
const machineLogName = "engine.jsonl"

// LogName returns the file name of a run's event log.
func LogName(run bus.ID) string { return "events-" + string(run) + ".jsonl" }

// runOfLog returns the run id a log file name carries, and whether the name is a run log's.
func runOfLog(name string) (bus.ID, bool) {
	id, ok := strings.CutPrefix(name, "events-")
	if !ok {
		return "", false
	}
	id, ok = strings.CutSuffix(id, ".jsonl")
	return bus.ID(id), ok && id != ""
}

// RepoDir returns a repository's metrics folder inside its git common directory.
func RepoDir(commonDir string) string { return filepath.Join(commonDir, "komodo", "metrics") }

// commonDir returns the absolute git common directory of the repository at root.
func commonDir(ctx context.Context, root string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("find the git directory of %s: %w", root, err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("find the git directory of %s: empty answer", root)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return filepath.Clean(dir), nil
}

// safeName reports whether id can be one path segment of a file name.
func safeName(id bus.ID) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(string(id), `/\`+"\x00")
}

// RunLog is an append-only log file; each line goes out in one write with no buffer between.
type RunLog struct {
	path string
	f    *os.File
}

// openLog opens the file at path for appending, creating its folder and the file.
func openLog(path string) (*RunLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("create folder of %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	l := &RunLog{path: path, f: f}
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

// Append writes line and a newline with one write call.
func (l *RunLog) Append(line []byte) error {
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	if _, err := l.f.Write(buf); err != nil {
		return fmt.Errorf("append to %s: %w", l.path, err)
	}
	return nil
}

// Close closes the file; closing twice is harmless.
func (l *RunLog) Close() error {
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
