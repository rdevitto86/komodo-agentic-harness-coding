package bus

import (
	"encoding/json"
	"slices"
	"testing"
)

// The schema lists exactly the event line's fields, and one variant per registered pair.
func TestSchemaCoversEnvelopeAndEveryPair(t *testing.T) {
	b, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Required []string
		OneOf    []json.RawMessage
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if want := []string{"type", "subtype", "run", "group", "time", "data"}; !slices.Equal(s.Required, want) {
		t.Fatalf("required %v, want %v", s.Required, want)
	}
	if n := len(EventKeys()); len(s.OneOf) != n {
		t.Fatalf("%d variants, want %d", len(s.OneOf), n)
	}
}
