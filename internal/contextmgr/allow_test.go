package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// AllowSkill turns a shipped skill on for one agent only, and always puts it in that agent's brief.
func TestAllowSkillPutsTheSkillInOneAgentsBrief(t *testing.T) {
	m := newManager(t)
	tree := newTree(map[string]string{"go.mod": "module x\n"})

	if err := m.AllowSkill("planner", "planning", true); err != nil {
		t.Fatal(err)
	}

	planner, err := m.GetContext(context.Background(), Request{Agent: "planner", Tree: tree, Slots: slotsFor("planner")})
	if err != nil {
		t.Fatal(err)
	}
	if text := planner.Brief.Text(); !strings.Contains(text, "## Standards: planning") ||
		!strings.Contains(text, "Order by need, not by habit") {
		t.Fatalf("the planner brief lacks the planning standard:\n%s", text)
	}

	builder, err := m.GetContext(context.Background(), Request{
		Agent: "builder", Tree: tree, Files: []string{"a.go"}, Slots: slotsFor("builder"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(builder.Brief.Text(), "Standards: planning") {
		t.Fatal("the builder brief carries the planning standard")
	}
}

func TestAllowSkillRefusesAnUnknownSkillOrAgent(t *testing.T) {
	m := newManager(t)
	if err := m.AllowSkill("planner", "no-such-skill", true); !errors.Is(err, ErrSkillUnknown) {
		t.Fatalf("unknown skill: %v", err)
	}
	if err := m.AllowSkill("nobody", "planning", true); !errors.Is(err, ErrAgentUnknown) {
		t.Fatalf("unknown agent: %v", err)
	}
}
