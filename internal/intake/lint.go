package intake

// Severity says whether a finding stops the plan or only warns.
type Severity string

// The two severities of a finding.
const (
	SeverityFail Severity = "fail"
	SeverityWarn Severity = "warn"
)

// Rule names the lint check that produced a finding.
type Rule string

// The eight lint checks.
const (
	RuleGrammar       Rule = "grammar"
	RuleRequired      Rule = "required"
	RuleWaveIsolation Rule = "wave_isolation"
	RuleProtected     Rule = "protected"
	RuleSizing        Rule = "sizing"
	RuleGraph         Rule = "graph"
	RuleEARS          Rule = "ears"
	RuleCaseCollision Rule = "case_collision"
)

// Finding is one failed or warned check, naming the rule and the file.
type Finding struct {
	Rule     Rule     `json:"rule"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`    // the file the rule failed on
	Message  string   `json:"message"` // what is wrong, in one sentence
}
