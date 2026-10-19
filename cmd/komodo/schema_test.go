package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// update rewrites embed/event.schema.json from the registry instead of comparing against it.
var update = flag.Bool("update", false, "rewrite embed/event.schema.json from the registry")

// schemaPath is the checked-in schema, relative to this package.
const schemaPath = "../../embed/event.schema.json"

// The checked-in schema equals the one built from every registered event; run with -update to rewrite it.
func TestSchemaMatchesEveryRegisteredEvent(t *testing.T) {
	registerAllEvents.Do(registerEvents)

	want, err := bus.Schema()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(schemaPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("embed/event.schema.json disagrees with the registered events; run: go test ./cmd/komodo -run " +
			"TestSchemaMatchesEveryRegisteredEvent -update")
	}
}

// Every registered type and subtype pair has a variant in the checked-in schema.
func TestSchemaFileNamesEveryRegisteredEvent(t *testing.T) {
	registerAllEvents.Do(registerEvents)

	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}

	var s struct {
		OneOf []struct {
			Properties struct {
				Type    struct{ Const bus.Type }
				Subtype struct{ Const bus.Subtype }
			}
		}
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}

	have := map[bus.Key]bool{}
	for _, v := range s.OneOf {
		have[bus.Key{Type: v.Properties.Type.Const, Subtype: v.Properties.Subtype.Const}] = true
	}
	for _, k := range bus.EventKeys() {
		if !have[k] {
			t.Errorf("%s / %s is registered but missing from embed/event.schema.json", k.Type, k.Subtype)
		}
	}
	if len(have) != len(bus.EventKeys()) {
		t.Errorf("the schema holds %d events, %d are registered", len(have), len(bus.EventKeys()))
	}
}
