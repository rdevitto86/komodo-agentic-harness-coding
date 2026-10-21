package agent

import "regexp"

// volatileToken matches what changes between two runs of the same test: a duration such as 0.21s, (0.00s) or 12ms, and the
// (cached) mark go test prints for a result served from its cache. A cached result is Go's own record of an earlier run
// of the same inputs, so matching it is as sound as matching a fresh run.
var volatileToken = regexp.MustCompile(`\(cached\)|\d+(?:\.\d+)?(?:ms|s)\b`)

// NormalizeOutput replaces the durations and cached marks of go test output with one fixed token, so that two runs of the
// same test compare equal.
func NormalizeOutput(s string) string { return volatileToken.ReplaceAllString(s, "<time>") }
