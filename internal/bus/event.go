// Package bus is the in-process event bus: publishers put events on it and subscribers read them by type.
package bus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Type is the coarse kind of an event, one of the 11 constants below.
type Type string

// Subtype is the finer kind inside a type, such as "session.end"; empty when the type needs no refinement.
type Subtype string

// ID is a version 7 universally unique identifier in text form.
type ID string

// Event is one line of the event stream; every event in the harness has this shape.
type Event struct {
	Type    Type      `json:"type"`
	Subtype Subtype   `json:"subtype"`
	Run     ID        `json:"run"`
	Group   ID        `json:"group"` // empty for run-level events
	Time    time.Time `json:"time"`  // stamped by Publish in Coordinated Universal Time, whole seconds
	Data    Payload   `json:"data"`  // the one struct registered for the type and subtype
}

// Payload is implemented by exactly one data struct per type and subtype pair.
type Payload interface{ EventKey() Key }

// Key names a type and subtype pair.
type Key struct {
	Type    Type
	Subtype Subtype
}

var (
	// ErrUnknownEvent marks a type and subtype pair that is not registered, or data that is not the registered struct.
	ErrUnknownEvent = errors.New("unknown event type, subtype or data")
	// ErrBusClosed marks a call on a bus that was closed.
	ErrBusClosed = errors.New("bus closed")
	// ErrBusSealed marks a publish on a bus that was sealed.
	ErrBusSealed = errors.New("bus sealed")
)

// wireEvent is the event line with its data body still undecoded.
type wireEvent struct {
	Type    Type            `json:"type"`
	Subtype Subtype         `json:"subtype"`
	Run     ID              `json:"run"`
	Group   ID              `json:"group"`
	Time    time.Time       `json:"time"`
	Data    json.RawMessage `json:"data"`
}

// UnmarshalJSON reads type and subtype first, then decodes data into the struct the registry returns for them.
// Fields the schema does not list are refused, in the envelope and in data.
func (e *Event) UnmarshalJSON(b []byte) error {
	var w wireEvent
	if err := decodeStrict(b, &w); err != nil {
		return fmt.Errorf("decode event envelope: %w", err)
	}

	newData, ok := lookup(Key{Type: w.Type, Subtype: w.Subtype})
	if !ok {
		return fmt.Errorf("decode event %q/%q: %w", w.Type, w.Subtype, ErrUnknownEvent)
	}
	if len(w.Data) == 0 || bytes.Equal(bytes.TrimSpace(w.Data), []byte("null")) {
		return fmt.Errorf("decode event %q/%q: data is missing", w.Type, w.Subtype)
	}

	p := newData()
	if err := decodeStrict(w.Data, p); err != nil {
		return fmt.Errorf("decode data of %q/%q: %w", w.Type, w.Subtype, err)
	}

	*e = Event{
		Type:    w.Type,
		Subtype: w.Subtype,
		Run:     w.Run,
		Group:   w.Group,
		Time:    w.Time,
		Data:    p,
	}
	return nil
}

// decodeStrict decodes one JSON value into v and refuses any field v does not declare.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
