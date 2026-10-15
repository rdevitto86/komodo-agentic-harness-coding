package state

import (
	"errors"
	"fmt"
)

// Errors the manager returns.
var (
	ErrStaleRev     = errors.New("record changed since it was read") // rev is not current; reread and retry
	ErrExists       = errors.New("record already exists")            // Create found a record
	ErrNotFound     = errors.New("record does not exist")            // no file stores the record
	ErrNotWriter    = errors.New("component may not write state")    // the writer check refused
	ErrNewerVersion = errors.New("record written by a newer komodo") // v above the current version
	ErrCorrupt      = errors.New("record failed a read check")       // the record could not be read as written
)

// InvalidError is returned for a write that breaks the schema or an invariant; the disk is untouched.
type InvalidError struct {
	Group string // the group id, or the kind of a machine-wide record
	Field string // path of the failing field
	Why   string // what is wrong with it
}

// Error describes the refusal as state_invalid with the group, the field and the reason.
func (e *InvalidError) Error() string {
	return fmt.Sprintf("state_invalid: %s: field %s: %s", e.Group, e.Field, e.Why)
}

// DiskError wraps a failed open, write, sync or rename; the disk keeps the old record.
type DiskError struct {
	Path string // the file the call touched
	Err  error  // the operating system error
}

// Error names the file and the operating system error.
func (e *DiskError) Error() string { return fmt.Sprintf("disk error on %s: %v", e.Path, e.Err) }

// Unwrap returns the operating system error.
func (e *DiskError) Unwrap() error { return e.Err }

// corrupt wraps a failed read check in ErrCorrupt, keeping the cause readable.
func corrupt(cause error) error { return fmt.Errorf("%w: %v", ErrCorrupt, cause) }
