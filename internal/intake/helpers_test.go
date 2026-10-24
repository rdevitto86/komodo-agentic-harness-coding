package intake

import "fmt"

// uid returns a version 7 identifier that differs per n.
func uid(n int) string { return fmt.Sprintf("0192e4c5-7a10-7d3e-9b2f-%012x", n) }

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
