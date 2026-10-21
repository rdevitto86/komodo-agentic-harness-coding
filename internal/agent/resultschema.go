package agent

import (
	"encoding/json"
	"reflect"
	"strings"
)

// resultEnums lists the words the Go validators accept for each named string type of a result.
var resultEnums = map[reflect.Type][]string{
	reflect.TypeFor[BuilderStatus](): {string(BuilderDone), string(BuilderNeedsScope)},
	reflect.TypeFor[Verdict]():       {string(VerdictMet), string(VerdictNotMet), string(VerdictBriefIncomplete)},
	reflect.TypeFor[Confirmation]():  {string(Confirmed), string(NotConfirmed)},
}

// resultSchema returns the JSON Schema a model's final call must match: the Go type's fields, closed.
func resultSchema(id string, t reflect.Type) json.RawMessage {
	node := typeSchema(t)
	node["$id"] = id

	b, err := json.Marshal(node)
	if err != nil {
		panic("marshal result schema: " + err.Error())
	}
	return b
}

// typeSchema returns the schema of a Go type as encoding/json reads it, using only plain draft features.
func typeSchema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.String:
		s := map[string]any{"type": "string"}
		if words, ok := resultEnums[t]; ok {
			s["enum"] = words
		}
		return s
	case reflect.Int:
		return map[string]any{"type": "integer"}
	case reflect.Pointer:
		return typeSchema(t.Elem())
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Struct:
		return objectSchema(t)
	}

	panic("no result schema for " + t.String())
}

// objectSchema returns the schema of a struct: its json fields with their desc tags, nothing else allowed.
func objectSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")

		p := typeSchema(f.Type)
		if d := f.Tag.Get("desc"); d != "" {
			p["description"] = d
		}

		props[name] = p
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}

	return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
}
