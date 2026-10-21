package agent

// DeepKind names one deep check.
type DeepKind string

const (
	DeepRace     DeepKind = "race"     // the language's race detector over the impact set
	DeepFuzz     DeepKind = "fuzz"     // random inputs against the changed functions
	DeepMutation DeepKind = "mutation" // deliberate edits to the changed functions
	DeepBench    DeepKind = "bench"    // the changed packages' benchmarks
)

// DeepPlan returns the deep checks that a risk level adds, in the order Go runs them.
func DeepPlan(l RiskLevel) []DeepKind {
	plan := []DeepKind{DeepRace}
	if l.Rank() >= RiskMedHigh.Rank() {
		plan = append(plan, DeepFuzz, DeepMutation, DeepBench)
	}
	return plan
}
