package agent

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

const modulePath = "github.com/rdevitto86/komodo-agentic-harness-coding"

func TestRolesAreTheFourRegistryRoles(t *testing.T) {
	var names []string
	for _, r := range Roles() {
		names = append(names, r.Name)
		if got, ok := RoleByName(r.Name); !ok || got.Name != r.Name {
			t.Errorf("RoleByName(%s) = %v", r.Name, ok)
		}
	}
	if !slices.Equal(names, []string{"planner", "builder", "tester", "reviewer"}) {
		t.Fatalf("roles = %v", names)
	}
	if _, ok := RoleByName("orchestrator"); ok {
		t.Error("an unknown role was found")
	}
}

func TestPlannerRole(t *testing.T) {
	r := PlannerRole()
	if r.Name != "planner" || r.PoolSlot || !slices.Equal(r.Tools, []string{"read", "search", "lint_backlog"}) {
		t.Fatalf("role = %+v", r)
	}
	if len(r.RunClasses) != 0 || len(r.Guard) != 0 || slices.Contains(r.Tools, "write") || slices.Contains(r.Tools, "run") {
		t.Errorf("planner holds powers it must not: %+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(b), `"run_classes":[]`) {
		t.Errorf("empty classes marshal as %s (%v)", b, err)
	}
}

func TestParsePlannerResult(t *testing.T) {
	raw := `{"files": [{"path": "epic-2/tg-2.1/TG.md", "content": "id: 0192e4c5-...\ngoal: x\n"}, {"path": ` +
		`"epic-2/tg-2.1/tsk-add-lock.md", "content": "files: a.go\n"}]}`
	r, err := ParsePlannerResult([]byte(raw))
	if err != nil || len(r.Files) != 2 || r.Files[0].Path != "epic-2/tg-2.1/TG.md" {
		t.Fatalf("parse: %+v %v", r, err)
	}
	for _, bad := range []string{
		`{"files": []}`,
		`{}`,
		`{"files": [{"path": " ", "content": "x"}]}`,
		`{"files": [{"path": "a", "content": "x", "extra": 1}]}`,
		raw + " x",
	} {
		if _, err := ParsePlannerResult([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// registryEntry is the part of an agent.json the compiled role must match.
type registryEntry struct {
	Tools      []string        `json:"tools"`
	RunClasses []string        `json:"run_classes"`
	Guard      []string        `json:"guard"`
	Slots      Slots           `json:"slots"`
	Result     json.RawMessage `json:"result"`
	PoolSlot   bool            `json:"pool_slot"`
}

func TestCompiledRolesMatchTheEmbeddedRegistry(t *testing.T) {
	for _, r := range Roles() {
		raw, err := komodo.Embedded.ReadFile("embed/agents/" + r.Name + "/agent.json")
		if err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		var e registryEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		if !slices.Equal(e.Tools, r.Tools) || !slices.Equal(e.RunClasses, r.RunClasses) || !slices.Equal(e.Guard, r.Guard) ||
			e.PoolSlot != r.PoolSlot {
			t.Errorf("%s: powers differ from the registry: %+v vs %+v", r.Name, e, r)
		}
		if !slices.Equal(e.Slots.Optional, r.Slots.Optional) {
			t.Errorf("%s: optional slots differ: %v vs %v", r.Name, e.Slots.Optional, r.Slots.Optional)
		}
		if !slices.Equal(e.Slots.Required, r.Slots.Required) {
			t.Errorf("%s: required slots differ: %v vs %v", r.Name, e.Slots.Required, r.Slots.Required)
		}
		var a, b any
		if json.Unmarshal(e.Result, &a) != nil || json.Unmarshal(r.Result, &b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s: result schema differs: %s vs %s", r.Name, e.Result, r.Result)
		}
	}
}

// importOrder lists the packages to the right of agent: the only internal packages it may import.
var importOrder = []string{
	"contextmgr",
	"llm",
	"sandbox",
	"resource",
	"metrics",
	"fault",
	"lease",
	"cache",
	"state",
	"config",
	"format",
	"bus",
	"core",
}

func TestImportsPointOnlyToTheRight(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v %v", files, err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == modulePath {
				continue
			}
			rest, ok := strings.CutPrefix(p, modulePath+"/internal/")
			if !ok {
				continue
			}
			if pkg, _, _ := strings.Cut(rest, "/"); !slices.Contains(importOrder, pkg) {
				t.Errorf("%s imports %s, which is not to the right of agent", f, p)
			}
		}
	}
}
