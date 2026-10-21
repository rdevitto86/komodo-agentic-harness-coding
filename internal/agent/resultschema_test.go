package agent

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

// update rewrites the result schema of every embed/agents/*/agent.json from its Go result type.
var update = flag.Bool("update", false, "rewrite the result schemas in embed/agents/*/agent.json from the Go result types")

// resultCases pairs every role with the Go type its final call decodes into and a sample a model could return.
var resultCases = []struct {
	role   string
	typ    reflect.Type
	sample string
	parse  func([]byte) error
}{
	{
		"planner", reflect.TypeOf(PlannerResult{}),
		`{"files": [{"path": "epic-2/tg-2.1/TG.md", "content": "goal: x\n"}]}`,
		func(b []byte) error { _, err := ParsePlannerResult(b); return err },
	},
	{
		"builder", reflect.TypeOf(BuilderResult{}),
		`{"status": "needs_scope", "summary": "s", "paths": ["a.go"], "reason": "r", "disputed": ["a.go:3"]}`,
		func(b []byte) error { _, err := ParseBuilderResult(b); return err },
	},
	{
		"qa", reflect.TypeOf(QAResult{}),
		`{"items": [{"item": "i1", "pass": false, "evidence": "e", "reason": "flaky"}],` +
			` "tasks": [{"task": "t1", "confirmation": "confirmed"}]}`,
		func(b []byte) error { _, err := ParseQAResult(b); return err },
	},
	{
		"reviewer", reflect.TypeOf(ReviewerResult{}),
		`{"verdicts": [{"task": "t1", "verdict": "brief_incomplete"}]}`,
		func(b []byte) error { _, err := ParseReviewerResult(b); return err },
	},
}

// embeddedResult returns the result schema of a role's embedded agent.json.
func embeddedResult(t *testing.T, role string) map[string]any {
	t.Helper()
	raw, err := komodo.Embedded.ReadFile("embed/agents/" + role + "/agent.json")
	if err != nil {
		t.Fatal(err)
	}

	var e registryEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}

	var s map[string]any
	if err := json.Unmarshal(e.Result, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Every Go field of a result type has a schema property of the same JSON name, and the reverse, at every depth.
func TestResultSchemaNamesEveryGoField(t *testing.T) {
	for _, c := range resultCases {
		var walk func(path string, typ reflect.Type, node map[string]any)
		walk = func(path string, typ reflect.Type, node map[string]any) {
			for typ.Kind() == reflect.Slice {
				typ = typ.Elem()
				items, _ := node["items"].(map[string]any)
				if node["type"] != "array" || items == nil {
					t.Errorf("%s %s: want an array with items, got %v", c.role, path, node)
					return
				}
				node = items
			}
			if typ.Kind() != reflect.Struct {
				return
			}

			props, _ := node["properties"].(map[string]any)
			if node["type"] != "object" || node["additionalProperties"] != false {
				t.Errorf("%s %s: want a closed object, got %v", c.role, path, node)
			}

			req, _ := node["required"].([]any)
			var names []string
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
				names = append(names, name)

				p, ok := props[name].(map[string]any)
				if !ok {
					t.Errorf("%s %s: Go field %s has no schema property %q", c.role, path, f.Name, name)
					continue
				}
				if d, _ := p["description"].(string); d == "" {
					t.Errorf("%s %s.%s: no description", c.role, path, name)
				}
				if want := !strings.Contains(opts, "omitempty"); slices.Contains(req, any(name)) != want {
					t.Errorf("%s %s.%s: required should be %v", c.role, path, name, want)
				}
				walk(path+"."+name, f.Type, p)
			}
			for name := range props {
				if !slices.Contains(names, name) {
					t.Errorf("%s %s: schema property %q has no Go field", c.role, path, name)
				}
			}
		}
		walk(c.role, c.typ, embeddedResult(t, c.role))
	}
}

// A sample shaped by the schema decodes into the Go type, and its top-level keys are schema properties.
func TestResultSchemaSampleDecodes(t *testing.T) {
	for _, c := range resultCases {
		if err := c.parse([]byte(c.sample)); err != nil {
			t.Errorf("%s: sample rejected: %v", c.role, err)
		}

		var sample map[string]any
		if err := json.Unmarshal([]byte(c.sample), &sample); err != nil {
			t.Fatal(err)
		}

		props := embeddedResult(t, c.role)["properties"].(map[string]any)
		for k := range sample {
			if _, ok := props[k]; !ok {
				t.Errorf("%s: sample key %q is not a schema property", c.role, k)
			}
		}
	}
}

// enumAt returns the enum of the property at a dotted path under a result schema, items stepped through.
func enumAt(t *testing.T, schema map[string]any, path string) []string {
	t.Helper()
	node := schema
	for _, name := range strings.Split(path, ".") {
		if items, ok := node["items"].(map[string]any); ok {
			node = items
		}

		props, _ := node["properties"].(map[string]any)
		next, ok := props[name].(map[string]any)
		if !ok {
			t.Fatalf("no property %s in %s", name, path)
		}
		node = next
	}

	var out []string
	vals, _ := node["enum"].([]any)
	for _, v := range vals {
		out = append(out, v.(string))
	}
	return out
}

// Each enum in a result schema lists exactly the words the Go code accepts for that field.
func TestResultSchemaEnumsMatchTheValidators(t *testing.T) {
	qa := func(field, v string) error {
		item := `{"item": "i", "pass": true, "evidence": "e"}`
		task := `{"task": "t", "confirmation": "confirmed"}`
		switch field {
		case "reason":
			item = fmt.Sprintf(`{"item": "i", "pass": false, "evidence": "e", "reason": %q}`, v)
		case "confirmation":
			task = fmt.Sprintf(`{"task": "t", "confirmation": %q}`, v)
		}

		r, err := ParseQAResult([]byte(`{"items": [` + item + `], "tasks": [` + task + `]}`))
		if err != nil {
			return err
		}
		return NewQA().AcceptResult([]string{"t"}, []string{"i"}, r)
	}

	cases := []struct {
		role, path string
		words      []string
		accepts    func(v string) error
	}{
		{"builder", "status", []string{"done", "needs_scope"}, func(v string) error {
			raw := `{"status": %q, "summary": "s", "paths": ["a"], "reason": "r"}`
			if v == "done" {
				raw = `{"status": %q, "summary": "s"}`
			}

			_, err := ParseBuilderResult([]byte(fmt.Sprintf(raw, v)))
			return err
		}},
		{"reviewer", "verdicts.verdict", []string{"met", "not_met", "brief_incomplete"}, func(v string) error {
			_, err := ParseReviewerResult([]byte(fmt.Sprintf(`{"verdicts": [{"task": "t", "verdict": %q}]}`, v)))
			return err
		}},
		{"qa", "tasks.confirmation", []string{"confirmed", "not_confirmed"}, func(v string) error {
			return qa("confirmation", v)
		}},
		{"qa", "items.reason", []string{"env", "wrong_command", "flaky", "real_failure"}, func(v string) error {
			return qa("reason", v)
		}},
	}
	for _, c := range cases {
		got := enumAt(t, embeddedResult(t, c.role), c.path)
		if !slices.Equal(got, c.words) {
			t.Errorf("%s %s enum = %v, want %v", c.role, c.path, got, c.words)
		}
		for _, w := range c.words {
			if err := c.accepts(w); err != nil {
				t.Errorf("%s %s: enum word %q is refused: %v", c.role, c.path, w, err)
			}
		}
		if err := c.accepts("bogus"); err == nil {
			t.Errorf("%s %s: a word outside the enum is accepted", c.role, c.path)
		}
	}
}

// The checked-in result schemas equal the ones built from the Go types; run with -update to rewrite them.
func TestResultSchemasMatchGoTypes(t *testing.T) {
	for _, r := range Roles() {
		path := "../../embed/agents/" + r.Name + "/agent.json"
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		start := bytes.Index(b, []byte(`"result": `)) + len(`"result": `)
		dec := json.NewDecoder(bytes.NewReader(b[start:]))
		var cur json.RawMessage
		if err := dec.Decode(&cur); err != nil {
			t.Fatal(err)
		}

		if *update {
			var want bytes.Buffer
			if err := json.Indent(&want, r.Result, "  ", "  "); err != nil {
				t.Fatal(err)
			}

			out := append(append(slices.Clone(b[:start]), want.Bytes()...), b[start+int(dec.InputOffset()):]...)
			if err := os.WriteFile(path, out, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}

		var a, w any
		if json.Unmarshal(cur, &a) != nil || json.Unmarshal(r.Result, &w) != nil || !reflect.DeepEqual(a, w) {
			t.Errorf("%s result schema disagrees with its Go type; run: go test ./internal/agent "+
				"-run TestResultSchemasMatchGoTypes -update", r.Name)
		}
	}
}
