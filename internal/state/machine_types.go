package state

import (
	"encoding/json"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// MachineKind names a machine-wide record; it is also the file's name without .json.
type MachineKind string

const (
	// probe results, limit reset times and link session counts
	KindProviders MachineKind = "providers"

	// the newest version seen and the one before the last swap
	KindUpdate MachineKind = "update"

	// every machine-wide file and entry that install owns
	KindInstall MachineKind = "install"

	// runs queued by komodo run --schedule
	KindSchedule MachineKind = "schedule"
)

// MachineHeader is the five fields every machine-wide record carries; the manager fills in all of
// them.
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
	// the model client's probe result, stored whole; the manager checks only that it is an object
	Probe json.RawMessage `json:"probe"`

	// when the link's limit lifts; zero when not limited
	ResetAt time.Time `json:"reset_at,omitzero"`

	// which cap was hit: window, weekly, model, spend or quota
	Cap string `json:"cap,omitempty"`

	// sessions running on the link across the machine
	Sessions int `json:"sessions"`

	// the Resource Manager's plan windows of the link, keyed by window name
	Windows map[string]WindowRecord `json:"windows,omitempty"`
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
	// absolute path of the file
	Target string `json:"target"`

	// for a settings file, which entry inside it
	Entry string `json:"entry,omitempty"`

	// SHA-256 of the content install wrote, 64 lowercase hexadecimal characters
	Hash string `json:"hash"`

	// number of the install step that wrote it
	Step int `json:"step"`
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
	// the (id) given to komodo run, such as ep-1 or tg-1.2
	Target string `json:"target"`

	// the repository root the target belongs to
	Root string `json:"root"`

	// the run id the engine minted when it queued the entry
	RunID string `json:"run_id"`

	// when the run starts
	At time.Time `json:"at"`

	// when komodo run --schedule queued it
	QueuedAt time.Time `json:"queued_at"`

	// the run-start data the CLI sent; published as is when the entry fires
	Start bus.RunStart `json:"start"`
}
