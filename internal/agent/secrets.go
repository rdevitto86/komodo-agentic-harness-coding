package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// diffLine is one added or removed line of a diff; Line is its number in the new file, 0 for a removed line.
type diffLine struct {
	File  string
	Line  int
	Text  string
	Added bool
}

// hunkHead matches a unified diff hunk header and captures the old count, new start and new count.
var hunkHead = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// diffLines lists the added and removed lines of a unified diff, using each hunk's counts to tell file headers from content.
// A +/- line outside any hunk still counts, so a diff without hunk headers is never read as empty.
func diffLines(diff string) []diffLine {
	var out []diffLine
	file := ""
	oldLeft, newLeft, newLine := 0, 0, 0

	for _, ln := range strings.Split(diff, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(ln, "+"):
				out = append(out, diffLine{file, newLine, ln[1:], true})
				newLine++
				newLeft--
				continue
			case strings.HasPrefix(ln, "-"):
				out = append(out, diffLine{file, 0, ln[1:], false})
				oldLeft--
				continue
			case strings.HasPrefix(ln, " ") || ln == "":
				newLine++
				newLeft--
				oldLeft--
				continue
			case strings.HasPrefix(ln, `\`):
				continue
			}
			oldLeft, newLeft = 0, 0
		}

		switch m := hunkHead.FindStringSubmatch(ln); {
		case strings.HasPrefix(ln, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(ln[4:], "b/"), "./")
			if ln[4:] == "/dev/null" {
				file = ""
			}
		case strings.HasPrefix(ln, "--- "):
		case m != nil:
			oldLeft, newLine, newLeft = countOr1(m[1]), countOr1(m[2]), countOr1(m[3])
		case strings.HasPrefix(ln, "+"):
			out = append(out, diffLine{file, 0, ln[1:], true})
		case strings.HasPrefix(ln, "-"):
			out = append(out, diffLine{file, 0, ln[1:], false})
		}
	}
	return out
}

// countOr1 reads a hunk count; a missing count means 1.
func countOr1(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(s)
	return n
}

// scanSecrets returns the file:line of each added line that matches a secret pattern; it never returns the line itself.
func scanSecrets(diff string, pats []*regexp.Regexp) []string {
	var out []string
	for _, d := range diffLines(diff) {
		if !d.Added {
			continue
		}
		for _, p := range pats {
			if p.MatchString(d.Text) {
				out = append(out, d.File+":"+strconv.Itoa(d.Line))
				break
			}
		}
	}
	return out
}
