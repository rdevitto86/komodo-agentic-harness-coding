package state

import (
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// MachineKind names a machine-wide record; it is also the file's name without .json.
type MachineKind string

const (
	KindProviders MachineKind = "providers" // probe results, limit reset times and link session counts
	KindUpdate    MachineKind = "update"    // the newest version seen and the one before the last swap
	KindInstall   MachineKind = "install"   // every machine-wide file and entry that install owns
	KindSchedule  MachineKind = "schedule"  // runs queued by komodo run --schedule
)

// MachineHeader is the five fields every machine-wide record carries; the manager fills in all of them.
type MachineHeader struct {
	V      int         `json:"v"`      // schema version the record was written under
	Kind   MachineKind `json:"kind"`   // which record this is; equals the file's name
	Rev    int         `json:"rev"`    // rises by 1 on every write
	TS     time.Time   `json:"ts"`     // when the manager wrote the record
	Writer string      `json:"writer"` // component and process of the writer
}

// MachineRecord is one stored machine-wide record as a reader receives it.
type MachineRecord[T any] struct {
	Header MachineHeader
	Body   T
}

// ProvidersBody is the body of providers.json.
type ProvidersBody struct {
	Links map[string]LinkRecord `json:"links"` // keyed by link name, such as claude-cli
}

// LinkRecord is one link's probe result, limit reset time and running session count.
type LinkRecord struct {
	Probe    json.RawMessage `json:"probe"`             // the model client's probe result, stored whole; the manager checks only that it is an object
	ResetAt  time.Time       `json:"reset_at,omitzero"` // when the link's limit lifts; zero when not limited
	Cap      string          `json:"cap,omitempty"`     // which cap was hit: window, weekly, model, spend or quota
	Sessions int             `json:"sessions"`          // sessions running on the link across the machine

	Windows map[string]WindowRecord `json:"windows,omitempty"` // the Resource Manager's plan windows of the link, keyed by window name
}

// WindowKey names one window on one link.
type WindowKey struct {
	Link   string
	Window string
}

// WindowRecord is one window's entry in providers.json.
type WindowRecord struct {
	PercentUsed float64   `json:"percent_used"`
	ResetAt     time.Time `json:"reset_at"`
	Sessions    int       `json:"sessions"` // sessions finished in the window
}

// UpdateBody is the body of update.json.
type UpdateBody struct {
	Latest    string    `json:"latest"`     // newest released version seen
	CheckedAt time.Time `json:"checked_at"` // when update last looked
	Previous  string    `json:"previous"`   // version bin/komodo held before the last swap
}

type InstallBody struct {
	Entries    []InstallEntry    `json:"entries"`    // one per owned machine-wide file or entry
	Selections InstallSelections `json:"selections"` // the optional parts a person chose
}

// InstallEntry records one owned file and the content install wrote.
type InstallEntry struct {
	Target string `json:"target"`          // absolute path of the file
	Entry  string `json:"entry,omitempty"` // for a settings file, which entry inside it
	Hash   string `json:"hash"`            // SHA-256 of the content install wrote, 64 lowercase hexadecimal characters
	Step   int    `json:"step"`            // number of the install step that wrote it
}

// InstallSelections are the optional parts chosen at install.
type InstallSelections struct {
	Hosts   []string `json:"hosts,omitempty"` // "claude", "codex"
	Project bool     `json:"project"`         // the --project files are owned
}

// ScheduleBody is the body of schedule.json.
type ScheduleBody struct {
	Entries []ScheduleEntry `json:"entries"` // queued runs, never null
}

// ScheduleEntry is one queued run.
type ScheduleEntry struct {
	Target   string       `json:"target"`    // the (id) given to komodo run, such as ep-1 or tg-1.2
	Root     string       `json:"root"`      // the repository root the target belongs to
	RunID    string       `json:"run_id"`    // the run id the engine minted when it queued the entry
	At       time.Time    `json:"at"`        // when the run starts
	QueuedAt time.Time    `json:"queued_at"` // when komodo run --schedule queued it
	Start    bus.RunStart `json:"start"`     // the run-start data the CLI sent; published as is when the entry fires
}
