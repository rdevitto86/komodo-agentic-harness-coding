package agent

const (
	PrecisionDesignPct  = 30 // a rule under this percent for PrecisionDesignDays is a design change
	PrecisionDesignDays = 60
)

// Precision counts one rule's findings for one model.
type Precision struct {
	Rule      string
	Model     string
	Submitted int // findings that passed submit_finding validation
	Verified  int // Go found the quote
	Fixed     int // the Builder fixed the finding
	Disputed  int // the Builder listed the finding's file:line in its result
}

// VerifiedPct returns the percent of submitted findings Go verified; 0 when none were submitted.
func (p Precision) VerifiedPct() float64 {
	if p.Submitted == 0 {
		return 0
	}
	return float64(p.Verified) * 100 / float64(p.Submitted)
}
