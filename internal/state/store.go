package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// writeFunc replaces a file with data; tests replace it to simulate a failing disk.
type writeFunc func(path string, data []byte) error

// syncFolder flushes a folder entry to disk; tests replace it to record the calls.
var syncFolder = core.SyncDir

// mkdirSynced makes a folder and any missing parents, then syncs the folder above each one it made.
func mkdirSynced(dir string) error {
	var made []string

	for d := dir; filepath.Dir(d) != d; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}

		made = append(made, d)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, d := range made {
		if err := syncFolder(filepath.Dir(d)); err != nil {
			return err
		}
	}

	return nil
}

// atomicWrite makes the file's folder, then writes a temporary file, syncs it and renames it over
// path.
func atomicWrite(path string, data []byte) error {
	if err := mkdirSynced(filepath.Dir(path)); err != nil {
		return err
	}

	return core.WriteFileAtomic(path, data, 0o644)
}

// put writes through w, trying once more after a failure, and wraps the last failure in a
// DiskError.
func put(w writeFunc, path string, data []byte) error {
	err := w(path, data)
	if err != nil {
		err = w(path, data)
	}

	if err != nil {
		return &DiskError{Path: path, Err: err}
	}

	return nil
}

// readRecord returns a file's bytes, ErrNotFound for an absent file and a DiskError for any other
// failure.
func readRecord(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrNotFound
	case err != nil:
		return nil, &DiskError{Path: path, Err: err}
	}

	return b, nil
}

// quarantine renames a damaged record to <name>.bad-<time> and returns the new path.
func quarantine(path string, now time.Time) (string, error) {
	base := path + ".bad-" + now.UTC().Format("20060102T150405Z")

	dst := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}

		dst = base + "-" + strconv.Itoa(i)
	}

	if err := os.Rename(path, dst); err != nil {
		return "", &DiskError{Path: path, Err: err}
	}

	return dst, nil
}

// sweepTemp deletes temporary files older than an hour from a folder; a name is kept unless it
// starts with the prefix.
func sweepTemp(dir, prefix string, now time.Time) {
	// staleTemp is the age after which a leftover temporary file is deleted.
	const staleTemp = time.Hour

	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range ents {
		if e.IsDir() ||
			!strings.HasPrefix(e.Name(), prefix) ||
			!strings.Contains(e.Name(), ".tmp-") {
			continue
		}

		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleTemp {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// decodeStrict decodes one JSON value into v and fails on an unknown field or trailing data.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return err
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("data after the record")
	}

	return nil
}

// versionOf reads the v field of a record without decoding the rest.
func versionOf(data []byte) (int, error) {
	var h struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return 0, err
	}

	if h.V == nil {
		return 0, errors.New("no version")
	}

	return *h.V, nil
}

// encodeRecord writes a record as indented JSON ending in a newline.
func encodeRecord(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(b, '\n'), nil
}

// stamp returns the time to the second in UTC, the form a header stores.
func stamp() time.Time { return time.Now().UTC().Truncate(time.Second) }
