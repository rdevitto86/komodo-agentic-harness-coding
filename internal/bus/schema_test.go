package bus

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// update rewrites embed/event.schema.json from the registry instead of comparing against it.
var update = flag.Bool("update", false, "rewrite embed/event.schema.json from the registry")

// schemaPath is the checked-in schema, relative to this package.
const schemaPath = "../../embed/event.schema.json"

var timeType = reflect.TypeOf(time.Time{})

// typeSchema returns the JSON Schema of a Go type as encoding/json writes it.
func typeSchema(t reflect.Type) map[string]any {
	switch {
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case t.Kind() == reflect.Pointer:
		return typeSchema(t.Elem())
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		// A nil slice without omitempty is written as null.
		return map[string]any{"type": []string{"array", "null"}, "items": typeSchema(t.Elem())}
	case reflect.Struct:
		return objectSchema(t)
	}
	panic("no schema for " + t.String())
}

// objectSchema returns the schema of a struct: its tagged fields, embedded structs flattened, nothing else allowed.
func objectSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if f.Anonymous && tag == "" {
				walk(f.Type)
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			props[name] = typeSchema(f.Type)
			if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
				required = append(required, name)
			}
		}
	}
	walk(t)
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
}

// buildSchema returns the schema of the event line for every pair the package itself registers.
func buildSchema() ([]byte, error) {
	var keys []Key
	for _, k := range registeredKeys() {
		if !strings.HasPrefix(string(k.Subtype), "test.") {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b Key) int {
		return strings.Compare(string(a.Type)+"\x00"+string(a.Subtype), string(b.Type)+"\x00"+string(b.Subtype))
	})
	defs := map[string]any{}
	var variants []any
	for _, k := range keys {
		newData, _ := lookup(k)
		typ := reflect.TypeOf(newData()).Elem()
		defs[typ.Name()] = objectSchema(typ)
		variants = append(variants, map[string]any{
			"properties": map[string]any{
				"type":    map[string]any{"const": k.Type},
				"subtype": map[string]any{"const": k.Subtype},
				"data":    map[string]any{"$ref": "#/$defs/" + typ.Name()},
			},
		})
	}
	env := objectSchema(reflect.TypeOf(wireEvent{}))
	props := env["properties"].(map[string]any)
	props["type"] = map[string]any{"enum": allTypes}
	props["data"] = map[string]any{"type": "object"}
	env["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	env["title"] = "Komodo event"
	env["oneOf"] = variants
	env["$defs"] = defs
	out, err := json.MarshalIndent(env, "", "  ")
	return append(out, '\n'), err
}

// The checked-in schema equals the one built from the registered Go structs; run with -update to rewrite it.
func TestSchemaMatchesGoTypes(t *testing.T) {
	want, err := buildSchema()
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
		t.Fatal(
			"embed/event.schema.json disagrees with the Go types; run: go test ./internal/bus -run " +
				"TestSchemaMatchesGoTypes -update",
		)
	}
}

// The schema lists exactly the event line's fields, and one variant per registered pair.
func TestSchemaCoversEnvelopeAndEveryPair(t *testing.T) {
	b, err := buildSchema()
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
	n := 0
	for _, k := range registeredKeys() {
		if !strings.HasPrefix(string(k.Subtype), "test.") {
			n++
		}
	}
	if len(s.OneOf) != n {
		t.Fatalf("%d variants, want %d", len(s.OneOf), n)
	}
}
