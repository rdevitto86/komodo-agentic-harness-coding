package state

import (
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
)

// machineKinds lists the four machine-wide records.
var machineKinds = []MachineKind{KindProviders, KindUpdate, KindInstall, KindSchedule}

// machineWriters lists the components that may write a machine-wide record.
var machineWriters = []string{"resource", "model", "cli", "engine"}

var (
	lockStale = 5 * time.Second      // age after which a record lock is taken over
	lockWait  = 10 * time.Second     // longest a writer waits for a record lock
	lockPoll  = 5 * time.Millisecond // pause between lock attempts
)

// Machine reads and writes the machine-wide records under the Komodo home folder.
type Machine struct {
	dir       string // <home>/state
	writer    string // the writer as given to NewMachine
	writeFile writeFunc

	mu  sync.Mutex // serialises this process's reads and writes
	rmw sync.Mutex // serialises this process's read-modify-write calls
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
	m := &Machine{dir: filepath.Join(home, "state"), writer: writer, writeFile: atomicWrite}
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

// lockRecord takes the lock file of one record by exclusive create, so two processes never check and write at once.
// It returns the function that releases it; a lock older than lockStale is taken over.
func (m *Machine) lockRecord(k MachineKind) (func(), error) {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, &DiskError{Path: m.dir, Err: err}
	}
	lp := m.path(k) + ".lock"
	for start := time.Now(); ; time.Sleep(lockPoll) {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lp) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, &DiskError{Path: lp, Err: err}
		}
		if st, serr := os.Stat(lp); serr == nil && time.Since(st.ModTime()) > lockStale {
			_ = os.Remove(lp)
			continue
		}
		if time.Since(start) > lockWait {
			return nil, &DiskError{Path: lp, Err: errors.New("another komodo process holds the record lock")}
		}
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
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
	if errors.Is(err, ErrCorrupt) && m.canWrite() {
		if _, qerr := quarantine(m.path(k), time.Now()); qerr != nil {
			return MachineRecord[T]{}, errors.Join(err, qerr)
		}
	}
	if err != nil {
		return MachineRecord[T]{}, err
	}
	body, err := convertBody[T](k, can)
	if err != nil {
		return MachineRecord[T]{}, err
	}
	return MachineRecord[T]{Header: h, Body: body}, nil
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

	m.mu.Lock()
	defer m.mu.Unlock()
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
	canon, err := json.Marshal(can)
	if err != nil {
		return zero, invalid(string(k), "body", err.Error())
	}
	b, err := encodeRecord(machineWire{MachineHeader: h, Body: canon})
	if err != nil {
		return zero, invalid(string(k), "body", err.Error())
	}
	if err := put(m.writeFile, m.path(k), b); err != nil {
		return zero, err
	}
	out, err := convertBody[T](k, can)
	if err != nil {
		return zero, err
	}
	return MachineRecord[T]{Header: h, Body: out}, nil
}
