// Package intake turns a request to plan into task lists: it reads the task-list files, lints them, runs the planner session
// and writes a plan that passed lint.
package intake

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Priority is a group's ordering hint.
type Priority string

// The two priorities a TG.md may name.
const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
)

// Epic is one docs/backlog/epic-N/ folder.
type Epic struct {
	Number int     // N in epic-N
	Groups []Group // one per tg-N.M folder, in folder-name order
}

// Group is one tg-N.M folder: TG.md plus its task files.
type Group struct {
	ID           string       // identifier from id:
	Name         string       // folder name, such as tg-1.2
	Goal         string       // one-line goal
	Points       int          // story points
	Status       string       // status: as last written
	Subsystem    string       // component name, or none
	Touches      []string     // globs the group expects to change
	Priority     Priority     // high or normal
	Files        []string     // files the builder may write
	Dependencies []string     // names of groups that must finish first
	Acceptance   []Acceptance // checks Quality Assurance runs
	Tasks        []Task       // at most 8
}

// Acceptance is one acceptance item.
type Acceptance struct {
	Keyword string   `json:"keyword"` // WHEN, IF or WHILE
	Text    string   `json:"text"`    // the item after the keyword, through its SHALL clause
	Cmd     string   `json:"cmd"`     // optional command Quality Assurance runs
	Needs   []string `json:"needs"`   // optional: network, docker or service:<name>
}

// Task is one tsk-*.md file.
type Task struct {
	Name          string   // file name without .md
	Files         []string // files the task names, at most 5
	DoneWhen      string   // command that proves the task done
	EstimateLines int      // planner's estimate of changed lines, at most 300
	Card          string   // the task's own write-up after the field block
}

// pathShape matches a task-list path under docs/backlog/: epic folder, group folder, then TG.md or a task file.
var pathShape = regexp.MustCompile(
	`^epic-([1-9]\d*)/(tg-([1-9]\d*)\.([1-9]\d*))/(TG\.md|(tsk-[^/]+)\.md)$`,
)

// epicFolder matches the name of an epic folder.
var epicFolder = regexp.MustCompile(`^epic-[1-9]\d*$`)

// groupParts collects the files of one group folder while a set of paths is read.
type groupParts struct {
	epic, index int
	name        string
	tg          *DraftFile
	tasks       []DraftFile
}

// Load reads docs/backlog under root; grammar problems come back as findings, not errors.
func Load(root string) ([]Epic, []Finding, error) {
	base := filepath.Join(root, "docs", "backlog")
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", base, err)
	}

	var files []DraftFile

	for _, e := range entries {
		if !e.IsDir() || !epicFolder.MatchString(e.Name()) {
			continue
		}

		err := filepath.WalkDir(
			filepath.Join(base, e.Name()),
			func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}

				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}

				rel, err := filepath.Rel(base, p)
				if err != nil {
					return err
				}

				files = append(files, DraftFile{Path: filepath.ToSlash(rel), Content: string(b)})
				return nil
			},
		)
		if err != nil {
			return nil, nil, fmt.Errorf("read task lists: %w", err)
		}
	}

	epics, found := parseFiles(files, false)
	return epics, append(found, strip(duplicateIDs(epics))...), nil
}

// parseFiles groups task-list files by group folder and parses each group; paths are relative to docs/backlog. With
// idOptional a TG.md may leave id out.
func parseFiles(files []DraftFile, idOptional bool) ([]Epic, []Finding) {
	var found []Finding

	groups := map[string]*groupParts{}

	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Path] {
			found = append(found, grammar(f.Path, "path appears twice in the draft"))
			continue
		}

		seen[f.Path] = true
		if msg := badPath(f.Path); msg != "" {
			found = append(found, grammar(f.Path, msg))
			continue
		}

		m := pathShape.FindStringSubmatch(f.Path)
		if m == nil {
			found = append(found, grammar(f.Path, "path is not epic-N/tg-N.M/TG.md or epic-N/tg-N.M/tsk-*.md"))
			continue
		}

		epic, _ := strconv.Atoi(m[1])
		groupEpic, _ := strconv.Atoi(m[3])

		idx, _ := strconv.Atoi(m[4])
		if epic != groupEpic {
			found = append(found, grammar(f.Path, fmt.Sprintf("group %s does not belong to epic-%d", m[2], epic)))
			continue
		}

		key := fmt.Sprintf("%d/%s", epic, m[2])
		g := groups[key]
		if g == nil {
			g = &groupParts{epic: epic, index: idx, name: m[2]}
			groups[key] = g
		}

		f := f
		if m[5] == "TG.md" {
			g.tg = &f
		} else {
			g.tasks = append(g.tasks, f)
		}
	}

	parts := make([]*groupParts, 0, len(groups))
	for _, g := range groups {
		parts = append(parts, g)
	}

	sort.Slice(parts, func(i, j int) bool {
		if parts[i].epic != parts[j].epic {
			return parts[i].epic < parts[j].epic
		}
		return parts[i].index < parts[j].index
	})

	var epics []Epic
	for _, p := range parts {
		g, gf := parseGroup(p, idOptional)
		found = append(found, gf...)
		if len(epics) == 0 || epics[len(epics)-1].Number != p.epic {
			epics = append(epics, Epic{Number: p.epic})
		}
		e := &epics[len(epics)-1]
		e.Groups = append(e.Groups, g)
	}

	return epics, found
}

// grammar returns a failing grammar finding.
func grammar(path, msg string) Finding {
	return Finding{Rule: RuleGrammar, Severity: SeverityFail, Path: path, Message: msg}
}

// badPath names what is wrong with a draft path before its shape is matched, or returns "".
func badPath(p string) string {
	switch {
	case p == "":
		return "path is empty"
	case strings.HasPrefix(p, "/") || filepath.IsAbs(p):
		return "path is absolute"
	case strings.Contains(p, "\\"):
		return "path holds a backslash"
	case slices.Contains(strings.Split(p, "/"), ".."):
		return "path holds .."
	}
	return ""
}

// parseGroup parses one group folder: its TG.md and its task files.
func parseGroup(p *groupParts, idOptional bool) (Group, []Finding) {
	g := Group{Name: p.name, Status: "todo", Subsystem: "none", Priority: PriorityNormal}
	tgPath := groupPath(p.epic, p.name)

	var found []Finding
	if p.tg == nil {
		found = append(found, grammar(tgPath, "group has no TG.md"))
	} else {
		found = append(found, parseTG(tgPath, p.tg.Content, &g, idOptional)...)
	}

	sort.Slice(p.tasks, func(i, j int) bool { return p.tasks[i].Path < p.tasks[j].Path })

	for _, t := range p.tasks {
		task, tf := parseTask(t.Path, t.Content)
		g.Tasks = append(g.Tasks, task)
		found = append(found, tf...)
	}
	if len(p.tasks) == 0 {
		found = append(found, grammar(tgPath, "group has no task file"))
	}

	return g, found
}

// groupPath returns the TG.md path of a group, relative to docs/backlog.
func groupPath(epic int, name string) string { return fmt.Sprintf("epic-%d/%s/TG.md", epic, name) }

// duplicateIDs reports each group whose id repeats an earlier group's id; the finding names both groups.
func duplicateIDs(epics []Epic) []scoped {
	var found []scoped

	first := map[string]string{}

	for _, e := range epics {
		for _, g := range e.Groups {
			if g.ID == "" {
				continue
			}
			if prev, ok := first[g.ID]; ok {
				found = append(found, scoped{
					grammar(
						groupPath(e.Number, g.Name),
						fmt.Sprintf("id %s is already the id of %s", g.ID, prev),
					),
					[]string{g.Name, prev},
				})
				continue
			}

			first[g.ID] = g.Name
		}
	}

	return found
}
