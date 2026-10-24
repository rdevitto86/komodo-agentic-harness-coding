//go:build !windows

package integration

import (
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// fixturePlan is the plan id the fixture's plan folder carries.
const fixturePlan = "ep-1"

// writePlan writes the blueprint of the fixture plan, with one group tg-1.1, into the rig's plans folder, where the
// command line resolves plan ids and group labels.
func (r *binRig) writePlan() {
	r.t.Helper()
	writeBlueprint(r.t, r.home, cli.RepoID(r.repo))
}

// writeBlueprint writes the fixture plan's blueprint for a repository id under a Komodo home.
func writeBlueprint(t *testing.T, home, repoID string) {
	t.Helper()
	err := intake.WriteBlueprint(home, intake.Blueprint{
		Plan:   fixturePlan,
		Repo:   intake.RepoFolder(repoID),
		Goal:   "the fixture plan",
		Groups: []intake.PlanGroup{{ID: groupID, Name: "tg-1.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
