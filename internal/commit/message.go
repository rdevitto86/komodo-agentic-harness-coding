package commit

import (
	"errors"
	"regexp"
	"strings"
)

// errEmptyMessage marks a commit message with no text.
var errEmptyMessage = errors.New("commit message is empty")

var (
	// trailerLine matches one Name: value line.
	trailerLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*:[ \t]+\S`)
	// paragraphBreak splits a message at blank lines.
	paragraphBreak = regexp.MustCompile(`\n[ \t]*\n+`)
)

// CheckMessage refuses an empty message and one whose last paragraph is only Name: value lines.
func CheckMessage(msg string) error {
	m := strings.TrimSpace(strings.ReplaceAll(msg, "\r\n", "\n"))
	if m == "" {
		return errEmptyMessage
	}
	paras := paragraphBreak.Split(m, -1)
	if len(paras) < 2 {
		return nil
	}
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		if !trailerLine.MatchString(line) {
			return nil
		}
	}
	return ErrTrailer
}
