//go:build !windows

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// goRepoTree is a read-only tree of one Go repository for the skill tests.
type goRepoTree struct{}

func (goRepoTree) Hash() string { return "skills-test" }

func (goRepoTree) Paths() []string { return []string{"go.mod", "internal/a/a.go"} }

func (goRepoTree) Exists(pattern string) bool {
	for _, p := range (goRepoTree{}).Paths() {
		if ok, _ := filepath.Match(pattern, p); ok {
			return true
		}
	}

	return false
}

func (goRepoTree) ReadFile(p string) ([]byte, error) {
	if p == "go.mod" {
		return []byte("module x\n"), nil
	}

	return nil, os.ErrNotExist
}

// skillKit opens a repository kit over a fresh git repository and returns its Context Manager.
func skillKit(t *testing.T) *contextmgr.Manager {
	t.Helper()

	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = root

	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, b)
	}

	t.Setenv("HOME", t.TempDir())

	s, err := newStack(t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx

	t.Cleanup(func() { cancel(); s.close() })

	k, err := s.kitFor(root, "", "repo")
	if err != nil {
		t.Fatal(err)
	}

	return k.cm
}

// skillSlots returns the caller values each agent's template requires.
func skillSlots(agent string) contextmgr.Values {
	switch agent {
	case "planner":
		return contextmgr.Values{contextmgr.Goal: "add a lock"}
	case "builder":
		return contextmgr.Values{
			contextmgr.Group: "tg-1", contextmgr.Tasks: "task list",
			contextmgr.Files: "internal/a/a.go", contextmgr.FileContents: "package a",
		}
	case "qa":
		return contextmgr.Values{contextmgr.Group: "tg-1", contextmgr.Tasks: "task list", contextmgr.Acceptance: "item 1"}
	}

	return contextmgr.Values{
		contextmgr.Group: "tg-1", contextmgr.Tasks: "task list", contextmgr.Files: "internal/a/a.go",
		contextmgr.Diff: "diff text", contextmgr.Lens: "correctness", contextmgr.LensRules: "rules",
		contextmgr.Checks: "clean",
	}
}

var standardsHeading = regexp.MustCompile(`(?m)^## Standards: (\S+)`)

// skillsInBrief returns the names of the skills one agent's brief carries for a group touching a Go file.
func skillsInBrief(t *testing.T, cm *contextmgr.Manager, agent string) []string {
	t.Helper()

	req := contextmgr.Request{Agent: agent, Tree: goRepoTree{}, Slots: skillSlots(agent)}
	if agent != "planner" {
		req.Run, req.Group, req.Files = "run", "tg-1", []string{"internal/a/a.go"}
	}

	got, err := cm.GetContext(context.Background(), req)
	if err != nil {
		t.Fatalf("%s brief: %v", agent, err)
	}

	var names []string
	for _, m := range standardsHeading.FindAllStringSubmatch(got.Brief.Text(), -1) {
		names = append(names, m[1])
	}

	return names
}

// Each agent's brief carries exactly the skills its agent.json allows and its files select.
func TestEachAgentGetsOnlyTheSkillsItsRegistryEntryAllows(t *testing.T) {
	cm := skillKit(t)

	want := map[string]string{
		"planner":  "planning",
		"builder":  "standards-comments standards-go",
		"qa":       "standards-go",
		"reviewer": "standards-go",
	}

	for agent, names := range want {
		if got := strings.Join(skillsInBrief(t, cm, agent), " "); got != names {
			t.Errorf("%s brief carries skills %q, want %q", agent, got, names)
		}
	}
}

// The builder's brief carries the Go standard text itself, not only its heading.
func TestBuilderBriefCarriesTheGoStandardText(t *testing.T) {
	cm := skillKit(t)

	got, err := cm.GetContext(context.Background(), contextmgr.Request{
		Agent: "builder", Run: "run", Group: "tg-1", Tree: goRepoTree{},
		Files: []string{"internal/a/a.go"}, Slots: skillSlots("builder"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Skills) != 2 {
		t.Fatalf("builder skills = %d, want standards-comments and standards-go", len(got.Skills))
	}

	text := got.Brief.Text()
	for _, s := range got.Skills {
		if !strings.Contains(text, s.Body) {
			t.Errorf("the builder brief lacks the body of %s", s.Name)
		}
	}
}

// A group with no Go file leaves the builder with only its always skill.
func TestGroupWithoutGoFilesSelectsOnlyAlwaysSkills(t *testing.T) {
	cm := skillKit(t)

	got, err := cm.GetContext(context.Background(), contextmgr.Request{
		Agent: "builder", Run: "run", Group: "tg-1", Tree: goRepoTree{},
		Files: []string{"README.md"}, Slots: skillSlots("builder"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Skills) != 1 || got.Skills[0].Name != "standards-comments" {
		t.Fatalf("builder skills = %v, want only standards-comments", got.Skills)
	}
}
