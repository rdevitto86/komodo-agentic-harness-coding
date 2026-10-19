package contextmgr

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// StartItems expands a trailing * against the shipped names, keeps always inside allow and leaves unused skills off.
func TestStartItemsExpandsPatternsAndEnablesOnlyAllowedSkills(t *testing.T) {
	items, err := StartItems(nil, map[string]Access{
		"planner": {Allow: []string{"planning"}, Always: []string{"planning"}},
		"builder": {Allow: []string{"standards-*"}, Always: []string{"standards-comments"}},
		"tester":  {Allow: []string{"standards-go", "no-such-skill"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	byName := map[SkillName]ItemState{}
	for _, it := range items {
		byName[it.Name] = it
	}

	if got := byName["standards-go"]; !got.Enabled || !slices.Equal(got.Allow, []string{"builder", "tester"}) ||
		len(got.Always) != 0 {
		t.Errorf("standards-go = %+v", got)
	}
	if got := byName["standards-comments"]; !slices.Equal(got.Always, []string{"builder"}) {
		t.Errorf("standards-comments = %+v", got)
	}
	if got := byName["planning"]; !slices.Equal(got.Allow, []string{"planner"}) ||
		!slices.Equal(got.Always, []string{"planner"}) {
		t.Errorf("planning = %+v", got)
	}
	if _, ok := byName["no-such-skill"]; ok {
		t.Error("a name outside the shipped skills became a row")
	}
}

// A narrowed allow list keeps every other agent out of the skill, and the builder gets only what it allows.
func TestStartItemsNeverReachesAnAgentOutsideItsAllowList(t *testing.T) {
	items, err := StartItems(nil, map[string]Access{"builder": {Allow: []string{"standards-go"}}})
	if err != nil {
		t.Fatal(err)
	}

	m := newManager(t, func(d *Deps) { d.Items = items })
	tree := goTree()

	for _, agent := range []string{"planner", "tester", "reviewer"} {
		got, err := m.GetContext(context.Background(), Request{
			Agent: agent, Tree: tree, Files: []string{"internal/a/a.go"}, Slots: slotsFor(agent),
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got.Brief.Text(), "## Standards:") {
			t.Errorf("%s brief carries a skill it is not allowed", agent)
		}
	}

	got, err := m.GetContext(context.Background(), Request{
		Agent: "builder", Tree: tree, Files: []string{"internal/a/a.go"}, Slots: slotsFor("builder"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "standards-go" {
		t.Fatalf("builder skills = %v, want only standards-go", got.Skills)
	}
}
