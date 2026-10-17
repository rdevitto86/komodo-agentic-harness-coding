package state

import (
	"bytes"
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
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// Machine reads and writes the machine-wide records under the Komodo home folder.
type Machine struct {
	// <home>/state
	dir string

	// the writer as given to NewMachine
	writer    string
	writeFile writeFunc

	// one write semaphore per record kind, taken before the file lock
	kindSem map[MachineKind]chan struct{}
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

// machineLockWait is the longest a writer waits for a record's file lock.
const machineLockWait = 2 * time.Second

var (
	// machineKinds lists the four machine-wide records.
	machineKinds = []MachineKind{KindProviders, KindUpdate, KindInstall, KindSchedule}

	// machineWriters lists the components that may write a machine-wide record.
	machineWriters = []string{"resource", "model", "cli", "engine"}
)

// afterBadRead runs between a read that failed its checks and the lock that confirms it; tests
// replace it.
var afterBadRead = func(MachineKind) {}

// NewMachine opens the manager over the machine's state folder; writer names the component allowed
// to write.
// It deletes temporary files older than an hour left by a killed write.
func NewMachine(home, writer string) (*Machine, error) {
	if home == "" {
		return nil, errors.New("home folder is empty")
	}

	m := &Machine{
		dir:       filepath.Join(home, "state"),
		writer:    writer,
		writeFile: atomicWrite,
		kindSem:   map[MachineKind]chan struct{}{},
	}

	for _, k := range machineKinds {
		m.kindSem[k] = make(chan struct{}, 1)
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

// lockRecord takes this process's write semaphore for the kind, then the operating-system lock of
// the record.
// Writers of one process queue on the semaphore, so only one polls the file lock; it returns the
// function that releases both.
func (m *Machine) lockRecord(ctx context.Context, k MachineKind) (func(), error) {
	sem := m.kindSem[k]
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if err := mkdirSynced(m.dir); err != nil {
		<-sem

		return nil, &DiskError{Path: m.dir, Err: err}
	}

	lp := m.path(k) + ".lock"

	unlock, err := core.FileLock(ctx, lp, machineLockWait)
	switch {
	case errors.Is(err, core.ErrLockWait):
		err = &DiskError{Path: lp, Err: errors.New("another komodo process holds the record lock")}
	case err != nil && ctx.Err() == nil:
		err = &DiskError{Path: lp, Err: err}
	}

	if err != nil {
		<-sem

		return nil, err
	}

	return func() { _ = unlock(); <-sem }, nil
}

// setAside moves a record that failed a read check out of the way; the caller holds the record
// lock.
// A record already moved is not a second failure.
func (m *Machine) setAside(k MachineKind, err error) error {
	if !errors.Is(err, ErrCorrupt) || !m.canWrite() {
		return err
	}

	_, qerr := quarantine(m.path(k), time.Now())
	if qerr != nil && !errors.Is(qerr, os.ErrNotExist) {
		return errors.Join(err, qerr)
	}

	return err
}

// store encodes a header and a canonical body once and writes them over the record's file.
func (m *Machine) store(k MachineKind, h MachineHeader, can any) error {
	b, err := encodeRecord(machineOut{MachineHeader: h, Body: can})
	if err != nil {
		return invalid(string(k), "body", err.Error())
	}

	return put(m.writeFile, m.path(k), b)
}

// readLocked reads a record under its lock and returns the canonical body and revision; an absent
// record is an empty body at rev 0.
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

// update reads a record under its lock, lets fn change the canonical body, checks it and stores it
// once.
// fn returns false when it changed nothing, and then nothing is written.
func (m *Machine) update(ctx context.Context, k MachineKind, fn func(can any) bool) error {
	if !m.canWrite() {
		return ErrNotWriter
	}

	unlock, err := m.lockRecord(ctx, k)
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

	h := MachineHeader{
		V:      Version,
		Kind:   k,
		Rev:    rev + 1,
		TS:     stamp(),
		Writer: m.headerWriter(),
	}

	return m.store(k, h, can)
}

// LoadMachine reads and checks a machine-wide record; a first run gets ErrNotFound and starts from
// an empty body.
// A record that fails its checks is set aside only after a reread under the record lock shows the
// same bytes.
func LoadMachine[T any](m *Machine, k MachineKind) (MachineRecord[T], error) {
	rec, bad, err := loadRecord[T](m, k)
	if bad == nil || !m.canWrite() {
		return rec, err
	}

	afterBadRead(k)

	unlock, lerr := m.lockRecord(context.Background(), k)
	if lerr != nil {
		return MachineRecord[T]{}, err
	}

	defer unlock()

	if again, rerr := readRecord(m.path(k)); rerr != nil || !bytes.Equal(again, bad) {
		rec, _, err = loadRecord[T](m, k)

		return rec, err
	}

	return MachineRecord[T]{}, m.setAside(k, err)
}

// SaveMachine replaces a machine-wide record after the checks; rev is the revision last read, or 0
// to create the record.
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
		return zero, invalid(
			string(k),
			"body",
			fmt.Sprintf("does not fit the %s record: %v", k, err),
		)
	}

	if bad := walkNil(reflect.ValueOf(can), "body", false); bad != "" {
		return zero, invalid(string(k), bad, "must be [] or {}, not null")
	}

	normalizeMachine(can)

	if err := validateMachine(k, can); err != nil {
		return zero, err
	}

	unlock, err := m.lockRecord(context.Background(), k)
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

// loadRecord reads and checks a record without moving anything; the bytes are returned when they
// failed a read check.
func loadRecord[T any](m *Machine, k MachineKind) (MachineRecord[T], []byte, error) {
	if newBody(k) == nil {
		return MachineRecord[T]{}, nil, invalid(string(k), "kind", "not a machine-wide record")
	}

	data, err := readRecord(m.path(k))
	if errors.Is(err, ErrNotFound) {
		empty, cerr := convertBody[T](k, newBody(k))
		if cerr != nil {
			return MachineRecord[T]{}, nil, cerr
		}

		return MachineRecord[T]{
			Header: MachineHeader{V: Version, Kind: k},
			Body:   empty,
		}, nil, ErrNotFound
	}

	if err != nil {
		return MachineRecord[T]{}, nil, err
	}

	h, can, err := decodeMachine(k, data)
	if err != nil {
		var bad []byte
		if errors.Is(err, ErrCorrupt) {
			bad = data
		}

		return MachineRecord[T]{}, bad, err
	}

	body, err := convertBody[T](k, can)
	if err != nil {
		return MachineRecord[T]{}, nil, err
	}

	return MachineRecord[T]{Header: h, Body: body}, nil, nil
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
		return out, invalid(
			string(k),
			"body",
			fmt.Sprintf("does not fit the %s record: %v", k, err),
		)
	}

	return out, nil
}

// decodeMachine turns stored bytes into a header and a checked canonical body; an older version is
// migrated in memory.
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
		return MachineHeader{}, nil, corrupt(
			fmt.Errorf("header names kind %q rev %d", w.Kind, w.Rev),
		)
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
