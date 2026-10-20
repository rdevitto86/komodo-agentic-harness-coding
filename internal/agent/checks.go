package agent

import "context"

// Step names one mechanical check.
type Step string

const (
	StepDoneWhen      Step = "done_when"
	StepScope         Step = "scope"
	StepSecrets       Step = "secrets"
	StepBuild         Step = "build"
	StepLint          Step = "lint"
	StepCoverage      Step = "coverage"
	StepBaseline      Step = "baseline"
	StepImpactSet     Step = "impact_set"
	StepFlake         Step = "flake"
	StepCaseCollision Step = "case_collision"
)

// Level is the weight of one step's result.
type Level string

const (
	LevelPass Level = "pass"
	LevelWarn Level = "warn" // reported; does not block
	LevelFail Level = "fail" // blocks; the group returns to the Builder
)

// StepResult is what one step found.
type StepResult struct {
	Step   Step     `json:"step"`
	Level  Level    `json:"level"`
	Detail []string `json:"detail,omitempty"` // failing test, file path or secret location
	Output string   `json:"output,omitempty"` // exact command output, capped like a gate result
}

// CheckResult is the outcome of one run of all steps.
type CheckResult struct {
	Steps []StepResult `json:"steps"`
	Flaky []string     `json:"flaky,omitempty"` // tests reported flaky
	// added executable lines no test ran, as file:line-range
	Uncovered []string `json:"uncovered,omitempty"`
}

// CheckInput is everything one run reads.
type CheckInput struct {
	Group    string
	Base     string            // base commit
	Head     string            // tree hash of the work under test
	Files    []string          // the group's effective file list
	Changed  []string          // paths changed between Base and Head
	DoneWhen map[string]string // task identifier to completion command
	Language string            // decides the impact set
}

// CheckDone is the data of the check.done event.
type CheckDone struct {
	Pass bool   `json:"pass"` // true when the result is clean
	Head string `json:"head"` // tree hash of the work under test
	// a test passed at the base commit and fails at head
	Regression bool         `json:"regression"`
	Steps      []StepResult `json:"steps"`
}

// Checker runs the mechanical checks on the work under test.
type Checker interface {
	Check(ctx context.Context, run CommandRunner, in CheckInput) (CheckResult, error)
}

// Clean reports whether no step has level fail; warnings do not count.
func (r CheckResult) Clean() bool {
	for _, s := range r.Steps {
		if s.Level == LevelFail {
			return false
		}
	}
	return true
}

// Done builds the check.done event data from a result and the head it checked.
func (r CheckResult) Done(head string) CheckDone {
	d := CheckDone{Pass: r.Clean(), Head: head, Steps: r.Steps}
	for _, s := range r.Steps {
		if s.Step == StepBaseline && s.Level == LevelFail {
			d.Regression = true
		}
	}
	return d
}
