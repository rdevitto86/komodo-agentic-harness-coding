package commit

import (
	"bytes"
	"fmt"
	"text/template"
)

const (
	// bodyTemplate is one group's section of the pull request body; every value comes from the
	// facts and the two verdicts.
	bodyTemplate = `## Summary
{{.Facts.DiffStat}}

## Tasks closed
{{range .Facts.TasksClosed}}- {{.}}
{{end}}
## Findings resolved
{{range .Facts.FindingCodes}}- {{.}}
{{end}}
## Quality Assurance
{{range .QA.Summary}}- {{.}}
{{end}}
## Reviewer
{{range .Reviewer.Summary}}- {{.}}
{{end}}`

	// epicBodyTemplate is the epic pull request body; every value comes from EpicFacts.
	epicBodyTemplate = `## Summary
{{.Goal}}

{{range .GroupGoals}}- {{.}}
{{end}}
## Changes
{{.DiffStat}}

{{range .Groups}}{{.}}
{{end}}
## Changelog
{{.Changelog}}`
)

var (
	groupBody = template.Must(template.New("group").Parse(bodyTemplate))
	epicBody  = template.Must(template.New("epic").Parse(epicBodyTemplate))
)

// RenderBody fills the Commit Handler's own pull request body template with the facts.
func RenderBody(f PRFacts, qa, reviewer Verdict) (string, error) {
	var b bytes.Buffer

	data := struct {
		Facts        PRFacts
		QA, Reviewer Verdict
	}{f, qa, reviewer}

	if err := groupBody.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render group body: %w", err)
	}

	return b.String(), nil
}

// RenderEpicBody fills the epic pull request body template with the epic's facts.
func RenderEpicBody(f EpicFacts) (string, error) {
	var b bytes.Buffer
	if err := epicBody.Execute(&b, f); err != nil {
		return "", fmt.Errorf("render epic body: %w", err)
	}

	return b.String(), nil
}
