package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrTampered reports a machine file that no longer matches its recorded checksum.
var ErrTampered = errors.New("a settings file was edited outside komodo config")

// crashHook is called at named points of a write; tests replace it to kill the process there.
var crashHook = func(stage string) {}

// Change is one key moving from an old value to a new one.
type Change struct {
	Key string
	Old any
	New any
}

// Batch is one successful write: every change from one SetConfigItem call.
type Batch struct {
	ID      string
	Time    time.Time
	Changes []Change
}

// logRow is one audit line of config.log.
type logRow struct {
	Time  string `json:"time"`
	Batch string `json:"batch"`
	Key   string `json:"key"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
}

// checksums is the content of config.sha: the checksum of each machine file, empty for a missing file.
type checksums struct {
	Settings string `json:"settings"`
	Agents   string `json:"agents"`
}

// machineFiles lists the machine file names in checksum order.
var machineFiles = []string{"settings.json", "agents.json"}

// shaPath returns the path of config.sha.
func (m *Manager) shaPath() string { return filepath.Join(m.stateDir(), "config.sha") }

// logPath returns the path of config.log.
func (m *Manager) logPath() string { return filepath.Join(m.stateDir(), "config.log") }

// fileHash returns the SHA-256 of a machine file in lowercase hexadecimal, or "" when it is absent.
func (m *Manager) fileHash(name string) (string, error) {
	b, err := readIfExists(filepath.Join(m.home, name))
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", name, err)
	}
	if b == nil {
		return "", nil
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// currentSums hashes both machine files as they are on disk.
func (m *Manager) currentSums() (checksums, error) {
	s, err := m.fileHash("settings.json")
	if err != nil {
		return checksums{}, err
	}
	a, err := m.fileHash("agents.json")
	return checksums{Settings: s, Agents: a}, err
}

// recordedSums reads config.sha; exists is false when it is absent and valid is false when it cannot be read.
func (m *Manager) recordedSums() (sums checksums, exists, valid bool) {
	b, err := readIfExists(m.shaPath())
	if err != nil {
		return checksums{}, true, false
	}
	if b == nil {
		return checksums{}, false, false
	}
	if json.Unmarshal(b, &sums) != nil {
		return checksums{}, true, false
	}
	return sums, true, true
}

// tamperedNow compares the machine files with config.sha once.
func (m *Manager) tamperedNow() (bool, error) {
	cur, err := m.currentSums()
	if err != nil {
		return true, err
	}
	rec, exists, valid := m.recordedSums()
	if !exists {
		return cur != checksums{}, nil
	}
	return !valid || rec != cur, nil
}

// Verify compares each machine file with its recorded checksum; ErrTampered on mismatch.
// A match is reread once after a pause while the writer lock is held; unreadable machine files are reported too.
func (m *Manager) Verify() error {
	bad, err := m.tamperedNow()
	if bad && err == nil && m.lockHeld() {
		time.Sleep(rereadWait)
		bad, err = m.tamperedNow()
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if bad {
		return ErrTampered
	}
	return m.resolve().readErr
}

// appendRows appends one audit line per change under one batch id.
func (m *Manager) appendRows(b Batch) error {
	var sb strings.Builder
	for _, c := range b.Changes {
		line, err := json.Marshal(logRow{Time: b.Time.Format(time.RFC3339), Batch: b.ID, Key: c.Key, Old: c.Old, New: c.New})
		if err != nil {
			return fmt.Errorf("encode audit row for %s: %w", c.Key, err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	f, err := os.OpenFile(m.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open config.log: %w", err)
	}
	if _, err := f.WriteString(sb.String()); err != nil {
		_ = f.Close()
		return fmt.Errorf("append to config.log: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync config.log: %w", err)
	}
	return f.Close()
}

// writeSums replaces config.sha with the checksums of the files as they now are.
func (m *Manager) writeSums() error {
	cur, err := m.currentSums()
	if err != nil {
		return err
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return fmt.Errorf("encode config.sha: %w", err)
	}
	return m.writeAtomic(m.shaPath(), append(b, '\n'))
}

// Accept rehashes the machine files after a hand edit and records the acceptance.
// With no machine files it deletes config.sha, which returns the guard to Defaults.
func (m *Manager) Accept() (Batch, error) {
	unlock, err := m.lock()
	if err != nil {
		return Batch{}, err
	}
	defer unlock()
	cur, err := m.currentSums()
	if err != nil {
		return Batch{}, err
	}
	if cur == (checksums{}) {
		if err := os.Remove(m.shaPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Batch{}, fmt.Errorf("delete config.sha: %w", err)
		}
		return Batch{}, nil
	}
	rec, _, _ := m.recordedSums()
	b := newBatch()
	for i, name := range machineFiles {
		oldSum, newSum := pick(rec, i), pick(cur, i)
		if oldSum != newSum {
			b.Changes = append(b.Changes, Change{Key: name, Old: oldSum, New: newSum})
		}
	}
	if len(b.Changes) == 0 {
		if _, exists, valid := m.recordedSums(); exists && valid {
			return Batch{}, nil
		}
	} else if err := m.appendRows(b); err != nil {
		return Batch{}, err
	}
	if err := m.writeSums(); err != nil {
		return Batch{}, err
	}
	if len(b.Changes) == 0 {
		return Batch{}, nil
	}
	return b, nil
}

// AcceptPlan lists the machine files Accept would record, those whose checksum differs from config.sha; it writes nothing.
// With no machine files it lists none, as Accept does.
func (m *Manager) AcceptPlan() ([]string, error) {
	cur, err := m.currentSums()
	if err != nil {
		return nil, err
	}
	if cur == (checksums{}) {
		return nil, nil
	}
	rec, _, _ := m.recordedSums()
	var out []string
	for i, name := range machineFiles {
		if pick(rec, i) != pick(cur, i) {
			out = append(out, name)
		}
	}
	return out, nil
}

// pick returns the settings checksum for index 0 and the agents checksum for index 1.
func pick(c checksums, i int) string {
	if i == 0 {
		return c.Settings
	}
	return c.Agents
}

// newBatch returns an empty batch with a fresh id and the current UTC time to the second.
func newBatch() Batch {
	return Batch{ID: newUUIDv7(), Time: time.Now().UTC().Truncate(time.Second)}
}

var (
	uuidMu   sync.Mutex
	uuidLast int64 // millisecond of the last id
	uuidSeq  int   // counter that keeps ids of one millisecond in order
)

// newUUIDv7 returns a version 7 UUID: 48 bits of milliseconds, then a counter and random bits.
func newUUIDv7() string {
	uuidMu.Lock()
	defer uuidMu.Unlock()
	ms := time.Now().UnixMilli()
	if ms <= uuidLast {
		ms = uuidLast
		uuidSeq++
	} else {
		uuidLast, uuidSeq = ms, 0
	}
	if uuidSeq > 0xfff {
		uuidLast++
		ms, uuidSeq = uuidLast, 0
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = 0x70 | byte(uuidSeq>>8)
	b[7] = byte(uuidSeq)
	b[8] = 0x80 | b[8]&0x3f
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
