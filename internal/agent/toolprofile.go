package agent

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// ToolProfile is one build tool's test commands for a language, read from the "tools" key of embed/langs/<language>.json.
type ToolProfile struct {
	Name     string   `json:"name"`     // gradle, maven or pytest
	Markers  []string `json:"markers"`  // build files that select the tool
	Test     string   `json:"test"`     // shell test command, used when the repository profile detects none
	JUnit    string   `json:"junit"`    // shell command that prints the JUnit XML reports of a finished Test run
	Coverage string   `json:"coverage"` // shell command that runs the tests and prints the JaCoCo or tool report
}

// ToolProfiles holds the tool entries of each language.
type ToolProfiles map[string][]ToolProfile

// ResolvedTools is the entry chosen for one repository, with the commands that apply to its test command.
type ResolvedTools struct {
	Name     string
	Test     string // set only when the repository profile detected no test command
	JUnit    string // empty when the repository's own test command is not the one the report command belongs to
	Coverage string
}

// langFamily maps a language to the language whose tool entries it shares.
var langFamily = map[string]string{"kotlin": "java"}

// LoadToolProfiles reads the tools of every embed/langs/*.json file; a file without the key adds nothing.
func LoadToolProfiles(fsys fs.FS) (ToolProfiles, error) {
	ents, err := fs.ReadDir(fsys, "embed/langs")
	if err != nil {
		return nil, fmt.Errorf("read language profiles: %w", err)
	}

	out := ToolProfiles{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		raw, err := fs.ReadFile(fsys, "embed/langs/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read language profile %s: %w", e.Name(), err)
		}

		var f struct {
			Tools []ToolProfile `json:"tools"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("decode %s: %w", e.Name(), err)
		}
		if len(f.Tools) > 0 {
			out[strings.TrimSuffix(e.Name(), ".json")] = f.Tools
		}
	}

	return out, nil
}

// Resolve picks the first entry of the language whose build file exists and fits it to the detected test command.
// The report command is kept only when the repository has no test command or its command is the entry's own.
func (p ToolProfiles) Resolve(language string, exists func(string) bool, detected []string) (ResolvedTools, bool) {
	tools, ok := p[language]
	if !ok {
		tools, ok = p[langFamily[language]]
	}

	for _, t := range tools {
		if !slices.ContainsFunc(t.Markers, exists) {
			continue
		}

		r := ResolvedTools{Name: t.Name, Coverage: t.Coverage}
		switch {
		case len(detected) == 0:
			r.Test, r.JUnit = t.Test, t.JUnit
		case strings.Join(detected, " ") == t.Test:
			r.JUnit = t.JUnit
		}
		return r, ok
	}

	return ResolvedTools{}, false
}
