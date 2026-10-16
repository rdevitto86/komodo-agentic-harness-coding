package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// helpFlag is one flag in the machine-readable help.
type helpFlag struct {
	Name    string `json:"name"`
	Short   string `json:"short,omitempty"`
	Kind    string `json:"kind"`
	Default string `json:"default,omitempty"`
	Help    string `json:"help"`
}

// helpDoc is one command in the machine-readable help.
type helpDoc struct {
	Name    string         `json:"name"`
	Summary string         `json:"summary"`
	Tier    string         `json:"tier"`
	Flags   []helpFlag     `json:"flags"`
	Result  map[string]any `json:"result"`
}

// resultTypes maps each command to the struct its data field holds.
var resultTypes = map[string]any{
	"install": InstallData{}, "config get": ConfigGetData{}, "config set": ConfigSetData{}, "plan": PlanData{},
	"run": RunData{}, "stop": StopData{}, "replay": ReplayData{}, "status": StatusData{}, "abandon": AbandonData{},
	"ui": EmptyData{}, "doctor": DoctorData{}, "update": UpdateData{}, "clean": CleanData{},
}

// EmptyData is the data of a command whose result is not built yet.
type EmptyData struct{}

// helpResult builds the help reply for the command, or for the whole table when words is empty.
func (c *CLI) helpResult(call *Call, words []string) Result {
	var rows []Command
	if call.Command != nil {
		rows = []Command{*call.Command}
	} else if len(words) == 1 {
		rows = []Command{*findCommand("config get"), *findCommand("config set")}
	} else {
		rows = commands()
	}
	if call.Global.JSON {
		return Result{Raw: helpJSON(rows)}
	}
	return Result{Raw: c.helpText(call, rows, words)}
}

// helpJSON renders one object per command: name, summary, tier, flags and the result schema.
func helpJSON(rows []Command) string {
	docs := make([]helpDoc, 0, len(rows))
	for _, r := range rows {
		tier := "shared"
		if r.Tier == TierTerminal {
			tier = "terminal-only"
		}
		d := helpDoc{Name: r.Name, Summary: r.Summary, Tier: tier, Flags: []helpFlag{}, Result: resultSchema(r.Name)}
		for _, f := range append(append([]FlagSpec{}, r.Flags...), globalSpecs...) {
			d.Flags = append(d.Flags, helpFlag(f))
		}
		docs = append(docs, d)
	}
	b, _ := json.MarshalIndent(docs, "", "  ")
	return string(b)
}

// helpText renders the plain help of the given rows; the key list follows for config levels.
func (c *CLI) helpText(call *Call, rows []Command, words []string) string {
	var b strings.Builder
	if call.Command == nil && len(words) == 0 {
		b.WriteString("komodo: the harness front door\n\nUsage: komodo <command> [flags]\n\nCommands:\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "  %-12s %s\n", r.Name, r.Summary)
		}
		b.WriteString("\nGlobal flags:\n")
		writeFlags(&b, globalSpecs)
		return b.String()
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "komodo %s: %s\n\nUsage: komodo %s [flags]\n", r.Name, r.Summary, r.Name)
		if len(r.Flags) > 0 {
			b.WriteString("\nFlags:\n")
			writeFlags(&b, r.Flags)
		}
		b.WriteString("\nGlobal flags:\n")
		writeFlags(&b, globalSpecs)
		b.WriteString("\n")
	}
	if strings.HasPrefix(call.name()+strings.Join(words, " "), "config") && c.deps.Config != nil {
		prefix := ""
		if len(call.Args) > 0 {
			prefix = call.Args[0]
		}
		b.WriteString("Keys:\n")
		for _, it := range c.deps.Config.ListConfigItems(prefix) {
			fmt.Fprintf(&b, "  %s\n", it.Key)
		}
	}
	return b.String()
}

// writeFlags writes one help line per flag.
func writeFlags(b *strings.Builder, specs []FlagSpec) {
	for _, f := range specs {
		name := "--" + f.Name
		if len(f.Name) == 1 {
			name = "-" + f.Name
		}
		if f.Kind != "bool" {
			name += " <" + f.Kind + ">"
		}
		fmt.Fprintf(b, "  %-24s %s\n", name, f.Help)
	}
}

// resultSchema returns the JSON Schema (draft 2020-12) of a command's data struct.
func resultSchema(name string) map[string]any {
	s := schemaOf(reflect.TypeOf(resultTypes[name]))
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	return s
}

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
)

// schemaOf builds the JSON Schema of a Go type from its field tags.
func schemaOf(t reflect.Type) map[string]any {
	switch t {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case rawType:
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaOf(t.Elem())
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Struct:
		return structSchema(t)
	}
	return map[string]any{}
}

// structSchema lists the exported fields that carry a json tag.
func structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" || name == "" {
			continue
		}
		props[name] = schemaOf(f.Type)
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
			required = append(required, name)
		}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
