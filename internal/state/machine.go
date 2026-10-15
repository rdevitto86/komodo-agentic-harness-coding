package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// machineKinds lists the four machine-wide records.
var machineKinds = []MachineKind{KindProviders, KindUpdate, KindInstall, KindSchedule}

// machineWriters lists the components that may write a machine-wide record.
var machineWriters = []string{"resource", "model", "cli", "engine"}

// machineLockWait is the longest a writer waits for a record's file lock.
const machineLockWait = 2 * time.Second

// Machine reads and writes the machine-wide records under the Komodo home folder.
type Machine struct {
	dir       string // <home>/state
	writer    string // the writer as given to NewMachine
	writeFile writeFunc

	kindMu map[MachineKind]*sync.Mutex // one write mutex per record kind, taken after the file lock
}

// machineOut is a machine-wide record as encoded: the header fields beside the body.
type machineOut struct {
	MachineHeader
	Body any `json:"body"`
}

// machineWire is a machine-wide record as stored: the header fields beside the raw body.
type machineWire struct {
	MachineHeader
	Body json.RawMessage `json:"body"`
}

// NewMachine opens the manager over the machine's state folder; writer names the component allowed to write.
// It deletes temporary files older than an hour left by a killed write.
func NewMachine(home, writer string) (*Machine, error) {
	if home == "" {
		return nil, errors.New("home folder is empty")
	}
	m := &Machine{dir: filepath.Join(home, "state"), writer: writer, writeFile: atomicWrite, kindMu: map[MachineKind]*sync.Mutex{}}
	for _, k := range machineKinds {
		m.kindMu[k] = &sync.Mutex{}
	}
	sweepTemp(m.dir, ".", time.Now())
	return m, nil
}

// canWrite reports whether this manager's writer is one of the four allowed components.
func (m *Machine) canWrite() bool { return slices.Contains(machineWriters, component(m.writer)) }

// path returns the file of one record.
func (m *Machine) path(k MachineKind) string { return filepath.Join(m.dir, string(k)+".json") }

// headerWriter returns the writer string a header carries: component and process.
func (m *Machine) headerWriter() string {
	if strings.Contains(m.writer, "/") {
		return m.writer
	}
	return m.writer + "/" + strconv.Itoa(os.Getpid())
}

// lockRecord takes the operating-system lock of one record, then this process's write mutex for the kind.
// It returns the function that releases both.
func (m *Machine) lockRecord(k MachineKind) (func(), error) {
	if err := mkdirSynced(m.dir); err != nil {
		return nil, &DiskError{Path: m.dir, Err: err}
	}
	lp := m.path(k) + ".lock"
	unlock, err := core.FileLock(context.Background(), lp, machineLockWait)
	if errors.Is(err, core.ErrLockWait) {
		return nil, &DiskError{Path: lp, Err: errors.New("another komodo process holds the record lock")}
	}
	if err != nil {
		return nil, &DiskError{Path: lp, Err: err}
	}
	mu := m.kindMu[k]
	mu.Lock()
	return func() { mu.Unlock(); _ = unlock() }, nil
}

// newBody returns an empty body struct of a kind, or nil for an unknown kind.
func newBody(k MachineKind) any {
	switch k {
	case KindProviders:
		return &ProvidersBody{}
	case KindUpdate:
		return &UpdateBody{}
	case KindInstall:
		return &InstallBody{}
	case KindSchedule:
		return &ScheduleBody{}
	}
	return nil
}

// LoadMachine reads and checks a machine-wide record; a first run gets ErrNotFound and starts from an empty body.
func LoadMachine[T any](m *Machine, k MachineKind) (MachineRecord[T], error) {
	if newBody(k) == nil {
		return MachineRecord[T]{}, invalid(string(k), "kind", "not a machine-wide record")
	}
	data, err := readRecord(m.path(k))
	if errors.Is(err, ErrNotFound) {
		empty, cerr := convertBody[T](k, newBody(k))
		if cerr != nil {
			return MachineRecord[T]{}, cerr
		}
		return MachineRecord[T]{Header: MachineHeader{V: Version, Kind: k}, Body: empty}, ErrNotFound
	}
	if err != nil {
		return MachineRecord[T]{}, err
	}
	h, can, err := decodeMachine(k, data)
	if err != nil {
		return MachineRecord[T]{}, m.setAside(k, err)
	}
	body, err := convertBody[T](k, can)
	if err != nil {
		return MachineRecord[T]{}, err
	}
	return MachineRecord[T]{Header: h, Body: body}, nil
}

// setAside moves a record that failed a read check out of the way when this manager may write; other errors pass through.
// A record another reader already moved is not a second failure.
func (m *Machine) setAside(k MachineKind, err error) error {
	if !errors.Is(err, ErrCorrupt) || !m.canWrite() {
		return err
	}
	if _, qerr := quarantine(m.path(k), time.Now()); qerr != nil && !errors.Is(qerr, os.ErrNotExist) {
		return errors.Join(err, qerr)
	}
	return err
}

// convertBody normalises a canonical body and decodes it into the caller's type.
func convertBody[T any](k MachineKind, can any) (T, error) {
	var out T
	walkNil(reflect.ValueOf(can), "body", true)
	normalizeMachine(can)
	raw, err := json.Marshal(can)
	if err != nil {
		return out, invalid(string(k), "body", err.Error())
	}
	if err := decodeStrict(raw, &out); err != nil {
		return out, invalid(string(k), "body", fmt.Sprintf("does not fit the %s record: %v", k, err))
	}
	return out, nil
}

// decodeMachine turns stored bytes into a header and a checked canonical body; an older version is migrated in memory.
func decodeMachine(k MachineKind, data []byte) (MachineHeader, any, error) {
	v, err := versionOf(data)
	if err != nil {
		return MachineHeader{}, nil, corrupt(err)
	}
	if v > Version {
		return MachineHeader{}, nil, ErrNewerVersion
	}
	if v < Version {
		if data, err = migrate(string(k), data, v); err != nil {
			return MachineHeader{}, nil, corrupt(err)
		}
	}
	var w machineWire
	if err := decodeStrict(data, &w); err != nil {
		return MachineHeader{}, nil, corrupt(err)
	}
	if w.Kind != k || w.Rev < 1 {
		return MachineHeader{}, nil, corrupt(fmt.Errorf("header names kind %q rev %d", w.Kind, w.Rev))
	}
	can := newBody(k)
	if err := decodeStrict(w.Body, can); err != nil {
		return MachineHeader{}, nil, corrupt(err)
	}
	walkNil(reflect.ValueOf(can), "body", true)
	normalizeMachine(can)
	if err := validateMachine(k, can); err != nil {
		return MachineHeader{}, nil, corrupt(err)
	}
	return w.MachineHeader, can, nil
}

// SaveMachine replaces a machine-wide record after the checks; rev is the revision last read, or 0 to create the record.
func SaveMachine[T any](m *Machine, k MachineKind, body T, rev int) (MachineRecord[T], error) {
	var zero MachineRecord[T]
	if !m.canWrite() {
		return zero, ErrNotWriter
	}
	can := newBody(k)
	if can == nil {
		return zero, invalid(string(k), "kind", "not a machine-wide record")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return zero, invalid(string(k), "body", err.Error())
	}
	if err := decodeStrict(raw, can); err != nil {
		return zero, invalid(string(k), "body", fmt.Sprintf("does not fit the %s record: %v", k, err))
	}
	if bad := walkNil(reflect.ValueOf(can), "body", false); bad != "" {
		return zero, invalid(string(k), bad, "must be [] or {}, not null")
	}
	normalizeMachine(can)
	if err := validateMachine(k, can); err != nil {
		return zero, err
	}

	unlock, err := m.lockRecord(k)
	if err != nil {
		return zero, err
	}
	defer unlock()
	h := MachineHeader{V: Version, Kind: k, Rev: 1, TS: stamp(), Writer: m.headerWriter()}
	data, err := readRecord(m.path(k))
	switch {
	case errors.Is(err, ErrNotFound):
		if rev != 0 {
			return zero, ErrStaleRev
		}
	case err != nil:
		return zero, err
	case rev == 0:
		return zero, ErrExists
	default:
		stored, _, derr := decodeMachine(k, data)
		if derr != nil {
			return zero, derr
		}
		if stored.Rev != rev {
			return zero, ErrStaleRev
		}
		h.Rev = stored.Rev + 1
	}
	if err := m.store(k, h, can); err != nil {
		return zero, err
	}
	out, err := convertBody[T](k, can)
	if err != nil {
		return zero, err
	}
	return MachineRecord[T]{Header: h, Body: out}, nil
}

// store encodes a header and a canonical body once and writes them over the record's file.
func (m *Machine) store(k MachineKind, h MachineHeader, can any) error {
	b, err := encodeRecord(machineOut{MachineHeader: h, Body: can})
	if err != nil {
		return invalid(string(k), "body", err.Error())
	}
	return put(m.writeFile, m.path(k), b)
}

// readLocked reads a record under its lock and returns the canonical body and revision; an absent record is an empty body at rev 0.
func (m *Machine) readLocked(k MachineKind) (any, int, error) {
	data, err := readRecord(m.path(k))
	if errors.Is(err, ErrNotFound) {
		empty := newBody(k)
		walkNil(reflect.ValueOf(empty), "body", true)
		return empty, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	h, can, err := decodeMachine(k, data)
	if err != nil {
		return nil, 0, m.setAside(k, err)
	}
	return can, h.Rev, nil
}

// update reads a record under its lock, lets fn change the canonical body, checks it and stores it once.
// fn returns false when it changed nothing, and then nothing is written.
func (m *Machine) update(k MachineKind, fn func(can any) bool) error {
	if !m.canWrite() {
		return ErrNotWriter
	}
	unlock, err := m.lockRecord(k)
	if err != nil {
		return err
	}
	defer unlock()
	can, rev, err := m.readLocked(k)
	if err != nil {
		return err
	}
	if !fn(can) {
		return nil
	}
	walkNil(reflect.ValueOf(can), "body", true)
	normalizeMachine(can)
	if err := validateMachine(k, can); err != nil {
		return err
	}
	return m.store(k, MachineHeader{V: Version, Kind: k, Rev: rev + 1, TS: stamp(), Writer: m.headerWriter()}, can)
}
