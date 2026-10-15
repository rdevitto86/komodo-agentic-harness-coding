package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LineFile is an append-only file of lines; each line goes out in one write with no buffer between.
type LineFile struct {
	path string
	f    *os.File
}

// OpenLineFile opens the file at path for appending, creating its folder and the file.
// A file that ends mid-line, as a dead process leaves it, gets that line ended first.
func OpenLineFile(path string) (*LineFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create folder of %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if endsMidLine(f) {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("end torn line of %s: %w", path, err)
		}
	}
	return &LineFile{path: path, f: f}, nil
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

// Path returns the file's path.
func (l *LineFile) Path() string { return l.path }

// Append writes line and a newline with one write call.
func (l *LineFile) Append(line []byte) error {
	if l.f == nil {
		return fmt.Errorf("append to %s: %w", l.path, os.ErrClosed)
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	if _, err := l.f.Write(buf); err != nil {
		return fmt.Errorf("append to %s: %w", l.path, err)
	}
	return nil
}

// Close closes the file; closing twice is harmless.
func (l *LineFile) Close() error {
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
