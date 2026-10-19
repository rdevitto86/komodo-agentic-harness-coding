package intake

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tgSpec is the fields of one TG.md; a zero field takes a valid default.
type tgSpec struct {
	id, goal, touches, points, status string
	files, deps, items                []string
	noDeps                            bool   // omit the dependencies key
	noID                              bool   // omit the id key, as a planner draft does
	extra                             string // an extra field line
}

// text renders the TG.md for a group numbered n.
func (s tgSpec) text(n int) string {
	if s.id == "" {
		s.id = uid(n)
	}
	if s.goal == "" {
		s.goal = "Group goal"
	}
	if s.points == "" {
		s.points = "3"
	}
	if s.status == "" {
		s.status = "todo"
	}
	if s.files == nil {
		s.files = []string{fmt.Sprintf("pkg%d/a.go", n)}
	}
	if s.items == nil {
		s.items = []string{"WHEN a thing happens THE SYSTEM SHALL respond"}
	}
	var b strings.Builder
	if !s.noID {
		fmt.Fprintf(&b, "id: %s\n", s.id)
	}
	fmt.Fprintf(&b, "goal: %s\nsubsystem: none\n", s.goal)
	if s.touches != "" {
		fmt.Fprintf(&b, "touches: %s\n", s.touches)
	}
	fmt.Fprintf(&b, "priority: normal\npoints: %s\nstatus: %s\nfiles:\n", s.points, s.status)
	for _, f := range s.files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	if !s.noDeps {
		b.WriteString("dependencies:\n")
		for _, d := range s.deps {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	if s.extra != "" {
		b.WriteString(s.extra + "\n")
	}
	b.WriteString("\n")
	for _, it := range s.items {
		fmt.Fprintf(&b, "- %s\n  cmd: go test ./...\n", it)
	}
	return b.String()
}

// uid returns a version 7 identifier that differs per n.
func uid(n int) string { return fmt.Sprintf("0192e4c5-7a10-7d3e-9b2f-%012x", n) }

// groupWithCheck returns the draft files of group tg-epic.n whose one task has the given done_when command.
func groupWithCheck(epic, n int, spec tgSpec, doneWhen string) []DraftFile {
	g := goodGroup(epic, n, spec)
	g[1].Content = taskText(100, doneWhen, fmt.Sprintf("pkg%d_%d/a.go", epic, n))
	return g
}

// goodGroup returns the draft files of group tg-epic.n with one task over its first file.
func goodGroup(epic, n int, spec tgSpec) []DraftFile {
	name := fmt.Sprintf("tg-%d.%d", epic, n)
	if spec.files == nil {
		spec.files = []string{fmt.Sprintf("pkg%d_%d/a.go", epic, n)}
	}
	return []DraftFile{
		{Path: fmt.Sprintf("epic-%d/%s/TG.md", epic, name), Content: spec.text(epic*100 + n)},
		{Path: fmt.Sprintf("epic-%d/%s/tsk-one.md", epic, name), Content: taskText(100, "go test ./...", spec.files[0])},
	}
}

// taskText renders a task file naming the given files.
func taskText(est int, doneWhen string, files ...string) string {
	var b strings.Builder
	b.WriteString("files:\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	if doneWhen != "" {
		fmt.Fprintf(&b, "done_when: %s\n", doneWhen)
	}
	fmt.Fprintf(&b, "estimate_lines: %d\n\nWrite the thing.\n", est)
	return b.String()
}

// writeBacklog writes draft-style files under root/docs/backlog.
func writeBacklog(t *testing.T, root string, files []DraftFile) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, "docs", "backlog", filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// rulesOf returns the findings with the given rule and severity.
func rulesOf(fs []Finding, r Rule, s Severity) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Rule == r && f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// fails returns the failing findings.
func fails(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity == SeverityFail {
			out = append(out, f)
		}
	}
	return out
}

// mustLoad loads root and fails the test on a read error.
func mustLoad(t *testing.T, root string) ([]Epic, []Finding) {
	t.Helper()
	e, f, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return e, f
}

// cat joins draft file lists.
func cat(parts ...[]DraftFile) []DraftFile { return draftOf(parts...).Files }

// draftOf collects files from several groups into one draft.
func draftOf(parts ...[]DraftFile) PlanDraft {
	var d PlanDraft
	for _, p := range parts {
		d.Files = append(d.Files, p...)
	}
	return d
}
