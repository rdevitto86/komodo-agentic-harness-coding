package state

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// update rewrites the checked-in schema files from the Go structs instead of comparing against them.
var update = flag.Bool("update", false, "rewrite embed/state.schema.json and embed/machine.schema.json from the Go structs")

const (
	stateSchemaFile   = "../../embed/state.schema.json"
	machineSchemaFile = "../../embed/machine.schema.json"
)

// typeSchema returns the JSON Schema of a Go type as encoding/json writes it, with enums from the checks.
func typeSchema(t reflect.Type, owner, field string) map[string]any {
	var s map[string]any
	switch {
	case t == timeType:
		s = map[string]any{"type": "string", "format": "date-time"}
	case t == rawType:
		s = map[string]any{"type": "object"}
	case t.Kind() == reflect.Pointer:
		return typeSchema(t.Elem(), owner, field)
	default:
		switch t.Kind() {
		case reflect.String:
			s = map[string]any{"type": "string"}
		case reflect.Bool:
			s = map[string]any{"type": "boolean"}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			s = map[string]any{"type": "integer"}
		case reflect.Float32, reflect.Float64:
			s = map[string]any{"type": "number"}
		case reflect.Slice:
			s = map[string]any{"type": "array", "items": typeSchema(t.Elem(), "", "")}
		case reflect.Map:
			s = map[string]any{"type": "object", "additionalProperties": typeSchema(t.Elem(), "", "")}
		case reflect.Struct:
			return objectSchema(t)
		default:
			panic("no schema for " + t.String())
		}
	}
	if vals, ok := enumValues(t, owner, field); ok {
		s["enum"] = vals
	}
	return s
}

// objectSchema returns the schema of a struct: its tagged fields, and nothing else allowed.
func objectSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		props[name] = typeSchema(f.Type, t.Name(), f.Name)
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
			required = append(required, name)
		}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
}

// enumValues returns the allowed strings of a field, from the field's own list or its type's.
func enumValues(t reflect.Type, owner, field string) ([]string, bool) {
	if v, ok := fieldEnums[owner+"."+field]; ok {
		return v, true
	}
	var out []string
	switch t {
	case reflect.TypeOf(Stage("")):
		for _, s := range stageValues {
			out = append(out, string(s))
		}
	case reflect.TypeOf(Reason("")):
		for _, r := range reasonValues {
			out = append(out, string(r))
		}
	case reflect.TypeOf(MachineKind("")):
		for _, k := range machineKinds {
			out = append(out, string(k))
		}
	default:
		return nil, false
	}
	return out, true
}

// withHeader joins a header struct's schema to a named payload schema.
func withHeader(header reflect.Type, payload string, body map[string]any, title string) map[string]any {
	s := objectSchema(header)
	s["properties"].(map[string]any)[payload] = body
	s["required"] = append(s["required"].([]string), payload)
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = title
	return s
}

// buildStateSchema returns the schema of the group record file.
func buildStateSchema() ([]byte, error) {
	s := withHeader(reflect.TypeOf(Header{}), "state", objectSchema(reflect.TypeOf(GroupState{})), "Komodo group record")
	out, err := json.MarshalIndent(s, "", "  ")
	return append(out, '\n'), err
}

// buildMachineSchema returns the schema of the four machine-wide record files, one variant per kind.
func buildMachineSchema() ([]byte, error) {
	bodies := map[MachineKind]reflect.Type{
		KindProviders: reflect.TypeOf(ProvidersBody{}), KindUpdate: reflect.TypeOf(UpdateBody{}),
		KindInstall: reflect.TypeOf(InstallBody{}), KindSchedule: reflect.TypeOf(ScheduleBody{}),
	}
	var variants []any
	defs := map[string]any{}
	for _, k := range machineKinds {
		bt := bodies[k]
		defs[bt.Name()] = objectSchema(bt)
		variants = append(variants, map[string]any{"properties": map[string]any{
			"kind": map[string]any{"const": k},
			"body": map[string]any{"$ref": "#/$defs/" + bt.Name()},
		}})
	}
	s := withHeader(reflect.TypeOf(MachineHeader{}), "body", map[string]any{"type": "object"}, "Komodo machine-wide record")
	s["oneOf"] = variants
	s["$defs"] = defs
	out, err := json.MarshalIndent(s, "", "  ")
	return append(out, '\n'), err
}

// Each checked-in schema equals the one built from the Go structs; run with -update to rewrite them.
func TestSchemasMatchGoTypes(t *testing.T) {
	for _, c := range []struct {
		path  string
		build func() ([]byte, error)
	}{{stateSchemaFile, buildStateSchema}, {machineSchemaFile, buildMachineSchema}} {
		want, err := c.build()
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if err := os.WriteFile(c.path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s disagrees with the Go types; run: go test ./internal/state -run TestSchemasMatchGoTypes -update", filepath.Base(c.path))
		}
	}
}

// The state schema lists every stage and reason, and requires the record's header fields.
func TestStateSchemaContent(t *testing.T) {
	b, err := buildStateSchema()
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	req := s["required"].([]any)
	for _, f := range []string{"v", "group", "rev", "ts", "writer", "state"} {
		if !slices.Contains(req, any(f)) {
			t.Errorf("required lacks %s", f)
		}
	}
	text := string(b)
	for _, want := range []string{`"test_review"`, `"blocked"`, `"state_invalid"`, `"cancelled"`} {
		if !strings.Contains(text, want) {
			t.Errorf("schema lacks %s", want)
		}
	}
}

// state imports only the bus, the core plumbing and the standard library.
func TestImportsPointRight(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	allowed := map[string]bool{"bus": true, "core": true}
	prefix := "github.com/rdevitto86/komodo-agentic-harness-coding"
	for _, f := range files {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			if rest := strings.TrimPrefix(p, prefix+"/internal/"); !allowed[rest] {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}
