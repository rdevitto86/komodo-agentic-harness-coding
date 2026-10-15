package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// addedLine is one line a diff adds, with its file and its line number in the new file.
type addedLine struct {
	File string
	Line int
	Text string
}

// hunkHead matches a unified diff hunk header and captures the old count, new start and new count.
var hunkHead = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// addedLines lists the lines a unified diff adds, using each hunk's counts to tell file headers from content.
func addedLines(diff string) []addedLine {
	var out []addedLine
	file := ""
	oldLeft, newLeft, newLine := 0, 0, 0
	for _, ln := range strings.Split(diff, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(ln, "+"):
				out = append(out, addedLine{file, newLine, ln[1:]})
				newLine++
				newLeft--
				continue
			case strings.HasPrefix(ln, "-"):
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
		if strings.HasPrefix(ln, "+++ ") {
			file = strings.TrimPrefix(strings.TrimPrefix(ln[4:], "b/"), "./")
			if ln[4:] == "/dev/null" {
				file = ""
			}
		} else if m := hunkHead.FindStringSubmatch(ln); m != nil {
			oldLeft, newLine, newLeft = countOr1(m[1]), countOr1(m[2]), countOr1(m[3])
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
	for _, a := range addedLines(diff) {
		for _, p := range pats {
			if p.MatchString(a.Text) {
				out = append(out, a.File+":"+strconv.Itoa(a.Line))
				break
			}
		}
	}
	return out
}
