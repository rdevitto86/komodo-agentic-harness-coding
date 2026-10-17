package commit

import (
	"bytes"
	"fmt"
	"text/template"
)

// bodyTemplate is one group's section of the pull request body; every value comes from the facts and the two verdicts.
const bodyTemplate = `## Summary
{{.Facts.DiffStat}}

## Tasks closed
{{range .Facts.TasksClosed}}- {{.}}
{{end}}
## Findings resolved
{{range .Facts.FindingCodes}}- {{.}}
{{end}}
## Tester
{{range .Tester.Summary}}- {{.}}
{{end}}
## Reviewer
{{range .Reviewer.Summary}}- {{.}}
{{end}}`

// epicBodyTemplate is the epic pull request body; every value comes from EpicFacts.
const epicBodyTemplate = `## Summary
{{.DiffStat}}

{{range .Groups}}{{.}}
{{end}}`

var (
	groupBody = template.Must(template.New("group").Parse(bodyTemplate))
	epicBody  = template.Must(template.New("epic").Parse(epicBodyTemplate))
)

// RenderBody fills the Commit Handler's own pull request body template with the facts.
func RenderBody(f PRFacts, tester, reviewer Verdict) (string, error) {
	var b bytes.Buffer
	data := struct {
		Facts            PRFacts
		Tester, Reviewer Verdict
	}{f, tester, reviewer}

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
