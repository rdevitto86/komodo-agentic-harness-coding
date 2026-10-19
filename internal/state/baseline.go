package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// baselineFile is the file beside a group's record that holds its baseline.
const baselineFile = "baseline.json"

// BaselineRecord is the result of the test class at one base commit, keyed by the commit's tree hash.
type BaselineRecord struct {
	V      int             `json:"v"`      // schema version the record was written under
	Tree   string          `json:"tree"`   // tree hash of the base commit
	Passed map[string]bool `json:"passed"` // test name to whether it passed at the base
	TS     time.Time       `json:"ts"`     // when the manager wrote the record
}

// SaveBaseline stores a group's baseline beside its record; only the engine may write it.
func (m *Manager) SaveBaseline(group string, b BaselineRecord) error {
	if !m.canWrite() {
		return ErrNotWriter
	}
	if !validKey(group) {
		return invalid(group, "group", "not usable as a folder name")
	}
	if b.Tree == "" {
		return invalid(group, "tree", "empty")
	}
	if b.Passed == nil {
		b.Passed = map[string]bool{}
	}

	b.V, b.TS = Version, stamp()

	raw, err := encodeRecord(b)
	if err != nil {
		return invalid(group, "baseline", err.Error())
	}

	l := m.lockFor(group)
	l.Lock()
	defer l.Unlock()

	return put(m.writeFile, m.baselinePath(group), raw)
}

// LoadBaseline reads a group's baseline; a damaged file is set aside and ErrCorrupt returned.
func (m *Manager) LoadBaseline(group string) (BaselineRecord, error) {
	if !validKey(group) {
		return BaselineRecord{}, ErrNotFound
	}

	l := m.lockFor(group)
	l.Lock()
	defer l.Unlock()

	b, err := m.peekBaseline(group)
	if errors.Is(err, ErrCorrupt) && m.canWrite() {
		if _, qerr := quarantine(m.baselinePath(group), time.Now()); qerr != nil {
			return BaselineRecord{}, errors.Join(err, qerr)
		}
	}

	return b, err
}

// FindBaseline returns the baseline of any group cut from the tree, so one run serves every group on the same base.
func (m *Manager) FindBaseline(tree string) (BaselineRecord, bool, error) {
	ents, err := os.ReadDir(filepath.Join(m.root, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return BaselineRecord{}, false, nil
	}
	if err != nil {
		return BaselineRecord{}, false, &DiskError{Path: filepath.Join(m.root, "runs"), Err: err}
	}

	for _, e := range ents {
		if !e.IsDir() || !validKey(e.Name()) {
			continue
		}

		b, err := m.peekBaseline(e.Name())
		switch {
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrCorrupt):
		case err != nil:
			return BaselineRecord{}, false, err
		case b.Tree == tree:
			return b, true, nil
		}
	}

	return BaselineRecord{}, false, nil
}

// peekBaseline reads and checks a group's baseline without writing.
func (m *Manager) peekBaseline(group string) (BaselineRecord, error) {
	raw, err := readRecord(m.baselinePath(group))
	if err != nil {
		return BaselineRecord{}, err
	}

	var b BaselineRecord
	if err := decodeStrict(raw, &b); err != nil {
		return BaselineRecord{}, corrupt(err)
	}
	if b.Tree == "" {
		return BaselineRecord{}, corrupt(fmt.Errorf("no tree hash"))
	}
	if b.V > Version {
		return BaselineRecord{}, ErrNewerVersion
	}
	if b.Passed == nil {
		b.Passed = map[string]bool{}
	}

	return b, nil
}

// baselinePath returns the file that stores a group's baseline.
func (m *Manager) baselinePath(group string) string {
	return filepath.Join(m.root, "runs", group, baselineFile)
}
