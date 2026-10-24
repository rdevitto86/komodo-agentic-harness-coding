//go:build !windows

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// The fixture plans are plan.md files in testdata/plans, parsed with intake.Parse like the planner's own output.
const (
	// fixturePlan is the plan id a pre-written fixture plan carries.
	fixturePlan = "plan-0a1b2c"

	// fixtureGroup is the label of the fixture plan's one group.
	fixtureGroup = "tg-1"

	// fixtureEpic is the epic branch the fixture plan's group lands on, without the komodo/ prefix.
	fixtureEpic = fixturePlan

	// tinyPlan is the plan.md of the tiny module: one group that adds an Add function.
	tinyPlan = "tiny.md"

	// calcPlan is the plan.md of the calc fixture repository: one group that adds Sub and Mul.
	calcPlan = "calc.md"
)

// planText reads a fixture plan.md from testdata/plans.
func planText(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", "plans", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// planBlueprint parses a fixture plan.md and fails the test on any grammar finding.
func planBlueprint(t *testing.T, name string) intake.Blueprint {
	t.Helper()

	b, found := intake.Parse(planText(t, name))
	if len(found) > 0 {
		t.Fatalf("fixture plan %s does not parse: %v", name, found)
	}

	return b
}

// writePlan writes the tiny fixture plan under the rig's Komodo home as plan fixturePlan.
func (r *binRig) writePlan() { r.writePlanFile(tinyPlan, nil) }

// writePlanFile parses a fixture plan.md, lets edit change the blueprint, and writes plan.md and blueprint.json into the
// plans folder of the rig's repository as plan fixturePlan, where the command line resolves plan ids and group labels.
func (r *binRig) writePlanFile(name string, edit func(*intake.Blueprint)) {
	r.t.Helper()

	b := planBlueprint(r.t, name)
	b.Plan, b.Repo = fixturePlan, intake.RepoFolder(cli.RepoID(r.repo))

	if edit != nil {
		edit(&b)
	}

	text := intake.Render(b)

	for i, g := range b.Groups {
		h, err := intake.CardHash(text, g)
		if err != nil {
			r.t.Fatal(err)
		}

		b.Groups[i].Hash = h
	}

	if err := intake.WriteBlueprint(r.home, b); err != nil {
		r.t.Fatal(err)
	}

	path := filepath.Join(intake.PlanDir(r.home, b.Repo, b.Plan), "plan.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// planPath returns the path of plan.md for a plan id of the rig's repository.
func (r *binRig) planPath(id string) string {
	return filepath.Join(intake.PlanDir(r.home, intake.RepoFolder(cli.RepoID(r.repo)), id), "plan.md")
}

// plansWritten lists the plan ids under the rig's plans folder, which `komodo plan` filled.
func (r *binRig) plansWritten() []string {
	r.t.Helper()

	dir := filepath.Dir(intake.PlanDir(r.home, intake.RepoFolder(cli.RepoID(r.repo)), "x"))
	ents, _ := os.ReadDir(dir)

	var ids []string

	for _, e := range ents {
		if e.IsDir() && e.Name()[0] != '.' {
			ids = append(ids, e.Name())
		}
	}

	return ids
}
