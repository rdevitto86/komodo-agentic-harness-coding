package agent

import "strings"

// Finding is one Reviewer claim, submitted with the submit_finding tool.
type Finding struct {
	Rule  string `json:"rule"`  // a rule identifier from a lens catalog or the standards rule list
	File  string `json:"file"`  // path from the worktree root
	Line  int    `json:"line"`  // line number from 1
	Quote string `json:"quote"` // the quoted line of evidence

	Proof      *Proof `json:"proof,omitempty"` // the Reviewer's own validation; a blocking finding needs one
	Confidence int    `json:"confidence"`      // 0 to 100: how sure the Reviewer is, after it validated the claim
}

// Proof is the Reviewer's own validation of a finding: what it ran or checked with its run tool, and what it saw.
// Go records a proof and never runs it.
type Proof struct {
	Ran string `json:"ran"` // the command run or the check made
	Saw string `json:"saw"` // the output or result that shows the problem
}

// Present reports whether a proof names what was run and what was seen.
func (p *Proof) Present() bool {
	return p != nil && strings.TrimSpace(p.Ran) != "" && strings.TrimSpace(p.Saw) != ""
}

// BlockConfidence is the least confidence at which a finding of a blocking level blocks.
const BlockConfidence = 80

// ValidConfidence reports whether n is a score from 0 to 100.
func ValidConfidence(n int) bool { return n >= 0 && n <= 100 }

// LineSource returns line n of a file.
type LineSource interface {
	Line(file string, n int) (string, bool)
}

type sourceFinding struct {
	name string
	ls   LineSource
}

// VerifyFinding reports where the quoted line is found, "head" or "diff", and whether it is.
// A line matches when it equals the quote after both are trimmed; a quote with a line break or no text never matches.
func VerifyFinding(f Finding, head, diff LineSource) (in string, ok bool) {
	want := strings.TrimSpace(f.Quote)
	if want == "" || strings.ContainsAny(f.Quote, "\r\n") {
		return "", false
	}

	for _, src := range []sourceFinding{{"head", head}, {"diff", diff}} {
		if src.ls == nil {
			continue
		}
		if got, found := src.ls.Line(f.File, f.Line); found && strings.TrimSpace(got) == want {
			return src.name, true
		}
	}

	return "", false
}
