package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// CardHash returns the card hash of one group: sha256 over its ## section of plan.md and every ### task section under it,
// with line endings normalised and trailing blank lines dropped.
func CardHash(planText string, g Group) (string, error) {
	_, secs := splitSections(planText)

	start := slices.IndexFunc(secs, func(s section) bool { return s.level == 2 && s.title == g.Name })
	if start < 0 {
		return "", fmt.Errorf("plan.md has no section for group %s", g.Name)
	}

	end := start + 1
	for end < len(secs) && secs[end].level == 3 {
		end++
	}

	var lines []string
	for _, s := range secs[start:end] {
		lines = append(lines, strings.Repeat("#", s.level)+" "+s.title)
		lines = append(lines, s.lines...)
	}

	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))

	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
