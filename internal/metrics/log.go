package metrics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// failuresFile is the machine metrics file of the Service's own failures that belong to no run's figures.
const failuresFile = "failures.jsonl"

// eventsDirOf returns the events folder that sits beside a repository's metrics folder, as the bus lays it out.
func eventsDirOf(dir string) string { return bus.EventsDir(filepath.Dir(filepath.Dir(dir))) }

// RepoDir returns a repository's metrics folder inside its git common directory.
func RepoDir(commonDir string) string { return filepath.Join(commonDir, "komodo", "metrics") }

// jsonlFile is an append-only line file; each line goes out in one write with no buffer between.
type jsonlFile struct {
	path string
	f    *os.File
}

// openLog opens the file at path for appending, creating its folder and the file.
func openLog(path string) (*jsonlFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("create folder of %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	l := &jsonlFile{path: path, f: f}
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
func (l *jsonlFile) Append(line []byte) error {
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	if _, err := l.f.Write(buf); err != nil {
		return fmt.Errorf("append to %s: %w", l.path, err)
	}
	return nil
}

// Close closes the file; closing twice is harmless.
func (l *jsonlFile) Close() error {
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
