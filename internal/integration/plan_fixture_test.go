//go:build !windows

package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/cli"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// fixturePlan is the plan id the fixture's plan folder carries.
const fixturePlan = "ep-1"

// fixtureBacklog is the folder of the fixture group inside the rig's repository.
const fixtureBacklog = "docs/backlog/epic-1/tg-1.1"

// writePlan writes the blueprint of the fixture plan, with one group tg-1.1 built from the repository's group files, into
// the rig's plans folder, where the command line resolves plan ids and group labels.
func (r *binRig) writePlan() {
	r.t.Helper()
	writeBlueprint(r.t, r.home, cli.RepoID(r.repo), filepath.Join(r.repo, filepath.FromSlash(fixtureBacklog)))
}

// writeBlueprint writes the fixture plan's blueprint for a repository id under a Komodo home; the group's goal, files,
// tasks and acceptance come from the TG.md and tsk-*.md files of dir.
func writeBlueprint(t *testing.T, home, repoID, dir string) {
	t.Helper()

	g := intake.Group{ID: groupID, Name: "tg-1.1", Priority: intake.PriorityNormal}

	if tg, err := os.ReadFile(filepath.Join(dir, "TG.md")); err == nil {
		fields, accept := splitFixture(string(tg))
		g.ID = strings.Join(fields["id"], "")
		g.Goal, g.Files, g.Subsystem = strings.Join(fields["goal"], ""), fields["files"], "none"
		g.Points, _ = strconv.Atoi(strings.Join(fields["points"], ""))

		names, _ := filepath.Glob(filepath.Join(dir, "tsk-*.md"))
		for i, n := range names {
			b, err := os.ReadFile(n)
			if err != nil {
				t.Fatal(err)
			}

			tf, _ := splitFixture(string(b))
			tk := intake.Task{
				Name:     strings.TrimSuffix(filepath.Base(n), ".md"),
				Files:    tf["files"],
				DoneWhen: strings.Join(tf["done_when"], ""),
			}
			tk.EstimateLines, _ = strconv.Atoi(strings.Join(tf["estimate_lines"], ""))

			if i == 0 {
				tk.Acceptance = accept
			}

			g.Tasks = append(g.Tasks, tk)
		}
	}

	err := intake.WriteBlueprint(home, intake.Blueprint{
		Plan:   fixturePlan,
		Repo:   intake.RepoFolder(repoID),
		Goal:   "the fixture plan",
		Groups: []intake.Group{g},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// splitFixture reads the key: value block of a fixture file into lists by key and the acceptance items after it.
func splitFixture(text string) (map[string][]string, []intake.Acceptance) {
	fields := map[string][]string{}

	var (
		accept []intake.Acceptance
		key    string
		body   bool
	)

	for _, line := range strings.Split(text, "\n") {
		switch {
		case !body && strings.TrimSpace(line) == "":
			body = true
		case body && strings.HasPrefix(line, "- "):
			kw, rest, _ := strings.Cut(strings.TrimSpace(line[2:]), " ")
			accept = append(accept, intake.Acceptance{Keyword: kw, Text: rest})
		case body && strings.HasPrefix(strings.TrimSpace(line), "cmd:") && len(accept) > 0:
			accept[len(accept)-1].Cmd = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "cmd:"))
		case !body && strings.HasPrefix(line, "- "):
			fields[key] = append(fields[key], strings.TrimSpace(line[2:]))
		case !body:
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}

			key = k
			if v = strings.TrimSpace(v); v != "" {
				fields[key] = []string{v}
			}
		}
	}

	return fields, accept
}
