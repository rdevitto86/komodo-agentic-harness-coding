package contextmgr

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The markers that fence the Komodo block inside AGENTS.md.
const (
	beginPrefix = "<!-- komodo:begin min="
	endMarker   = "<!-- komodo:end -->"
	agentsFile  = "AGENTS.md"
)

// extractAddendum removes the Komodo block from AGENTS.md text and drops every paragraph with a steering phrase.
// Broken block markers drop the whole addendum.
func extractAddendum(md string, phrases []string) (string, []Warning) {
	if strings.TrimSpace(md) == "" {
		return "", nil
	}
	lines := strings.Split(md, "\n")
	begin, end, nb, ne := -1, -1, 0, 0
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, beginPrefix) && strings.HasSuffix(t, "-->"):
			nb++
			if begin < 0 {
				begin = i
			}
		case t == endMarker:
			ne++
			if end < 0 {
				end = i
			}
		}
	}
	if nb != ne || nb > 1 || (nb == 1 && begin > end) {
		return "", []Warning{{Code: "addendum_markers_broken", Path: agentsFile, Note: "the Komodo block markers are missing, repeated or out of order; the addendum is dropped"}}
	}
	if nb == 1 {
		lines = append(append([]string(nil), lines[:begin]...), lines[end+1:]...)
	}
	var kept []string
	var warns []Warning
	for _, p := range paragraphs(lines) {
		if phrase, hit := findPhrase(p, phrases); hit {
			warns = append(warns, Warning{Code: "addendum_paragraph_dropped", Path: agentsFile, Note: "paragraph dropped for the steering phrase \"" + phrase + "\""})
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "\n\n"), warns
}

// paragraphs groups lines into blank-line separated paragraphs; a code fence stays in one paragraph.
func paragraphs(lines []string) []string {
	var out, cur []string
	fence := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, l := range lines {
		if isFence(l) {
			fence = !fence
		}
		if !fence && strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, strings.TrimRight(l, "\r"))
	}
	flush()
	return out
}

// normalizeText lowercases text and turns each run of whitespace into one space.
func normalizeText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// isWordRune reports whether r is part of a word.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// findPhrase returns the first listed phrase the text holds, matching without letter case and on word boundaries.
func findPhrase(text string, phrases []string) (string, bool) {
	norm := normalizeText(text)
	for _, p := range phrases {
		np := normalizeText(p)
		if np == "" {
			continue
		}
		for from := 0; ; {
			i := strings.Index(norm[from:], np)
			if i < 0 {
				break
			}
			i += from
			j := i + len(np)
			before, after := rune(' '), rune(' ')
			if i > 0 {
				before, _ = utf8.DecodeLastRuneInString(norm[:i])
			}
			if j < len(norm) {
				after, _ = utf8.DecodeRuneInString(norm[j:])
			}
			if !isWordRune(before) && !isWordRune(after) {
				return p, true
			}
			from = i + 1
		}
	}
	return "", false
}
