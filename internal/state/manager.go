package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// engineComponent is the one component that may write a group record.
const engineComponent = "engine"

// Manager reads and writes the group records of one repository.
type Manager struct {
	root      string // <git-common-dir>/komodo
	writer    string // the writer as given to New
	writeFile writeFunc

	mu    sync.Mutex
	locks map[string]*sync.Mutex // one lock per group
}

// wire is the group record as stored: the header fields beside the state.
type wire struct {
	Header
	State GroupState `json:"state"`
}

// New opens the manager over the repository's Komodo folder; writer names the component allowed to write.
// It deletes temporary files older than an hour left by a killed write.
func New(root, writer string) (*Manager, error) {
	if root == "" {
		return nil, errors.New("state folder is empty")
	}
	m := &Manager{root: root, writer: writer, writeFile: atomicWrite, locks: map[string]*sync.Mutex{}}
	runs := filepath.Join(root, "runs")
	ents, err := os.ReadDir(runs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, &DiskError{Path: runs, Err: err}
	}
	for _, e := range ents {
		if e.IsDir() {
			sweepTemp(filepath.Join(runs, e.Name()), ".state.json", time.Now())
		}
	}
	return m, nil
}

// component returns the part of a writer name before the first slash.
func component(writer string) string {
	c, _, _ := strings.Cut(writer, "/")
	return c
}

// canWrite reports whether this manager's writer may change group records.
func (m *Manager) canWrite() bool { return component(m.writer) == engineComponent }

// path returns the file that stores a group's record.
func (m *Manager) path(group string) string {
	return filepath.Join(m.root, "runs", group, "state.json")
}

// lockFor returns the lock that serialises calls for one group.
func (m *Manager) lockFor(group string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[group]
	if !ok {
		l = &sync.Mutex{}
		m.locks[group] = l
	}
	return l
}

// headerWriter returns the writer string a header carries: component, run and process.
func (m *Manager) headerWriter(run string) string {
	if strings.Contains(m.writer, "/") {
		return m.writer
	}
	return m.writer + "/" + run + "/" + strconv.Itoa(os.Getpid())
}

// Create writes a group's first record at rev 1; it refuses a zero fence and an existing record.
func (m *Manager) Create(group string, g GroupState) (Record, error) {
	return m.write(group, g, 0, true)
}

// Save replaces the record after the checks; rev must be the revision last read.
func (m *Manager) Save(group string, g GroupState, rev int) (Record, error) {
	return m.write(group, g, rev, false)
}

// write runs the writer, revision, schema and invariant checks, then stores the record.
func (m *Manager) write(group string, g GroupState, rev int, create bool) (Record, error) {
	if !m.canWrite() {
		return Record{}, ErrNotWriter
	}
	if !validKey(group) {
		return Record{}, invalid(group, "group", "not usable as a folder name")
	}
	l := m.lockFor(group)
	l.Lock()
	defer l.Unlock()

	stored, err := m.peek(group)
	switch {
	case create && err == nil:
		return Record{}, ErrExists
	case create && errors.Is(err, ErrNotFound):
	case err != nil:
		return Record{}, err
	case stored.Header.Rev != rev:
		return Record{}, ErrStaleRev
	}
	c, err := canonicalGroup(group, g)
	if err != nil {
		return Record{}, err
	}
	h := Header{V: Version, Group: group, Rev: 1, TS: stamp(), Writer: m.headerWriter(c.Run)}
	if !create {
		if err := checkMonotone(group, &stored.State, &c); err != nil {
			return Record{}, err
		}
		h.Rev = stored.Header.Rev + 1
	}
	b, err := encodeRecord(wire{Header: h, State: c})
	if err != nil {
		return Record{}, invalid(group, "state", err.Error())
	}
	if err := put(m.writeFile, m.path(group), b); err != nil {
		return Record{}, err
	}
	return Record{Header: h, State: c}, nil
}

// canonicalGroup returns a private, checked copy of g in its stored form.
func canonicalGroup(group string, g GroupState) (GroupState, error) {
	for i, a := range g.ActionsOpen {
		if _, ok := objectOrEmpty(a.Args); !ok {
			return GroupState{}, invalid(group, fmt.Sprintf("state.actions_open[%d].args", i), "not a JSON object")
		}
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return GroupState{}, invalid(group, "state", err.Error())
	}
	var c GroupState
	if err := json.Unmarshal(raw, &c); err != nil {
		return GroupState{}, invalid(group, "state", err.Error())
	}
	normalizeGroup(&c)
	if err := validateGroup(group, &c); err != nil {
		return GroupState{}, err
	}
	return c, nil
}

// Load reads and checks a record; a damaged record is set aside and ErrCorrupt returned.
func (m *Manager) Load(group string) (Record, error) {
	if !validKey(group) {
		return Record{}, ErrNotFound
	}
	l := m.lockFor(group)
	l.Lock()
	defer l.Unlock()
	r, err := m.peek(group)
	if errors.Is(err, ErrCorrupt) && m.canWrite() {
		if _, qerr := quarantine(m.path(group), time.Now()); qerr != nil {
			return Record{}, errors.Join(err, qerr)
		}
	}
	return r, err
}

// Peek reads and checks a record without ever writing; a damaged record is reported, not moved.
func (m *Manager) Peek(group string) (Record, error) {
	if !validKey(group) {
		return Record{}, ErrNotFound
	}
	return m.peek(group)
}

// peek reads, migrates and checks the record of a group.
func (m *Manager) peek(group string) (Record, error) {
	data, err := readRecord(m.path(group))
	if err != nil {
		return Record{}, err
	}
	return decodeGroup(group, data)
}

// decodeGroup turns stored bytes into a checked record; an older version is migrated in memory.
func decodeGroup(group string, data []byte) (Record, error) {
	v, err := versionOf(data)
	if err != nil {
		return Record{}, corrupt(err)
	}
	if v > Version {
		return Record{}, ErrNewerVersion
	}
	if v < Version {
		if data, err = migrate("", data, v); err != nil {
			return Record{}, corrupt(err)
		}
	}
	var w wire
	if err := decodeStrict(data, &w); err != nil {
		return Record{}, corrupt(err)
	}
	if w.Group != group || w.Rev < 1 {
		return Record{}, corrupt(fmt.Errorf("header names group %q rev %d", w.Group, w.Rev))
	}
	walkNil(reflect.ValueOf(&w.State), "state", true)
	normalizeGroup(&w.State)
	if err := validateGroup(group, &w.State); err != nil {
		return Record{}, corrupt(err)
	}
	return Record{Header: w.Header, State: w.State}, nil
}

// Counters reads only the counters of a group.
func (m *Manager) Counters(group string) (Counters, error) {
	if !validKey(group) {
		return Counters{}, ErrNotFound
	}
	data, err := readRecord(m.path(group))
	if err != nil {
		return Counters{}, err
	}
	v, err := versionOf(data)
	if err != nil {
		return Counters{}, corrupt(err)
	}
	if v > Version {
		return Counters{}, ErrNewerVersion
	}
	if v < Version {
		if data, err = migrate("", data, v); err != nil {
			return Counters{}, corrupt(err)
		}
	}
	var part struct {
		State struct {
			Counters Counters `json:"counters"`
		} `json:"state"`
	}
	if err := json.Unmarshal(data, &part); err != nil {
		return Counters{}, corrupt(err)
	}
	c := part.State.Counters
	if c.Blocks == nil {
		c.Blocks = map[string]int{}
	}
	return c, nil
}

// List reads every group record, returning good records with the first error.
func (m *Manager) List() ([]Record, error) {
	runs := filepath.Join(m.root, "runs")
	ents, err := os.ReadDir(runs)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, &DiskError{Path: runs, Err: err}
	}
	recs := []Record{}
	var first error
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r, err := m.Peek(e.Name())
		switch {
		case err == nil:
			recs = append(recs, r)
		case errors.Is(err, ErrNotFound):
		case first == nil:
			first = fmt.Errorf("group %s: %w", e.Name(), err)
		}
	}
	return recs, first
}

// Remove deletes a record after the writer and revision checks.
func (m *Manager) Remove(group string, rev int) error {
	if !m.canWrite() {
		return ErrNotWriter
	}
	if !validKey(group) {
		return ErrNotFound
	}
	l := m.lockFor(group)
	l.Lock()
	defer l.Unlock()
	stored, err := m.peek(group)
	switch {
	case err != nil:
		return err
	case stored.Header.Rev != rev:
		return ErrStaleRev
	}
	if err := os.Remove(m.path(group)); err != nil {
		return &DiskError{Path: m.path(group), Err: err}
	}
	return nil
}

// Quarantined lists the set-aside records.
func (m *Manager) Quarantined() ([]string, error) {
	out, err := filepath.Glob(filepath.Join(m.root, "runs", "*", "state.json.bad-*"))
	if err != nil {
		return nil, err
	}
	return out, nil
}
