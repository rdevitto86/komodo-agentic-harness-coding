package contextmgr

// Counter returns the token count of a text.
type Counter func(text string) int

// SkillTokenLimit is the largest skill body, in tokens by the bundled counter.
const SkillTokenLimit = 6000

// CountTokens is the bundled estimate: one token per 4 bytes, rounded up.
func CountTokens(text string) int { return (len(text) + 3) / 4 }

// counterOr returns c, or the bundled counter when c is nil.
func counterOr(c Counter) Counter {
	if c == nil {
		return CountTokens
	}
	return c
}
