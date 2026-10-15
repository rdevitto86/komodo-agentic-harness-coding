package format

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// wrapWidth is the column count a line wraps at.
const wrapWidth = 80

// Colour escape codes: bold and dim on, and the one code that ends both.
const (
	boldOn   = "\x1b[1m"
	dimOn    = "\x1b[2m"
	styleOff = "\x1b[22m"
)

// segment is a run of a line that is either plain text or one whole code span.
type segment struct {
	text   string
	isSpan bool
}

// timeType is the one struct type the escaper leaves alone.
var timeType = reflect.TypeOf(time.Time{})

// span returns s as a Markdown code span: newlines escaped, fenced one backtick longer than its longest run.
func span(s string) string {
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("\r", `\n`, "\n", `\n`).Replace(s)
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	pad := s[0] == '`' || s[len(s)-1] == '`' ||
		(s[0] == ' ' && s[len(s)-1] == ' ' && strings.Trim(s, " ") != "")
	if pad {
		s = " " + s + " "
	}
	return fence + s + fence
}

// unspan returns the text inside a code span, or s unchanged when s is not one whole span.
func unspan(s string) string {
	k := runLen(s, 0)
	if k == 0 || len(s) < 2*k+1 || runLen(s, len(s)-k) != k || (len(s)-k > 0 && s[len(s)-k-1] == '`') {
		return s
	}
	inner := s[k : len(s)-k]
	if len(inner) >= 2 && inner[0] == ' ' && inner[len(inner)-1] == ' ' && strings.Trim(inner, " ") != "" {
		inner = inner[1 : len(inner)-1]
	}
	return inner
}

// runLen returns the length of the backtick run that starts at s[i], 0 when s[i] is not a backtick.
func runLen(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// splitSpans cuts a line into plain text and whole code spans; an unclosed backtick run stays text.
func splitSpans(s string) []segment {
	var out []segment
	start, i := 0, 0
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		k := runLen(s, i)
		end := closeRun(s, i+k, k)
		if end < 0 {
			i += k
			continue
		}
		if i > start {
			out = append(out, segment{s[start:i], false})
		}
		out = append(out, segment{s[i : end+k], true})
		i, start = end+k, end+k
	}
	if start < len(s) {
		out = append(out, segment{s[start:], false})
	}
	return out
}

// closeRun returns the index of the next backtick run of exactly k from `from`, or -1.
func closeRun(s string, from, k int) int {
	for p := from; p < len(s); {
		if s[p] != '`' {
			p++
			continue
		}
		r := runLen(s, p)
		if r == k {
			return p
		}
		p += r
	}
	return -1
}

// escapeFacts replaces every string reachable from v with its code-span form, keys of string maps included.
func escapeFacts(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			escapeFacts(v.Elem())
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(span(v.String()))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			escapeFacts(v.Index(i))
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				escapeFacts(v.Field(i))
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.IsNil() {
			return
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			val := reflect.New(v.Type().Elem()).Elem()
			val.Set(it.Value())
			if val.Kind() == reflect.Interface && !val.IsNil() {
				val.Set(reflect.ValueOf(escapeAny(val.Interface())))
			} else if val.Kind() == reflect.String {
				val.SetString(span(val.String()))
			}
			key := reflect.ValueOf(span(it.Key().String())).Convert(v.Type().Key())
			out.SetMapIndex(key, val)
		}
		v.Set(out)
	}
}

// escapeAny returns a decoded JSON value with every string, and every key of a nested object, in code-span form.
func escapeAny(x any) any {
	switch t := x.(type) {
	case string:
		return span(t)
	case json.Number:
		return t
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = escapeAny(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[span(k)] = escapeAny(e)
		}
		return out
	}
	return x
}

// isFence reports whether a line opens or closes a fenced block.
func isFence(line string) bool {
	t := strings.TrimLeft(line, " ")
	return len(line)-len(t) <= 3 && strings.HasPrefix(t, "```") && !strings.Contains(strings.TrimLeft(t, "`"), "`")
}

// isTableLine reports whether a line belongs to a Markdown table.
func isTableLine(line string) bool { return strings.HasPrefix(strings.TrimLeft(line, " "), "|") }

// tidy trims trailing spaces, collapses blank runs to one blank line and ends the text with one newline; fenced blocks stay as written.
func tidy(text string) string {
	var out []string
	fenced, blank := false, false
	for _, line := range strings.Split(text, "\n") {
		if isFence(line) {
			fenced = !fenced
		} else if fenced {
			out = append(out, line)
			continue
		}
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank = true
			continue
		}
		if blank && len(out) > 0 {
			out = append(out, "")
		}
		blank = false
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// wrap breaks lines over width columns at spaces outside code spans; tables and fenced blocks are left whole.
func wrap(text string, width int) string {
	lines := strings.Split(text, "\n")
	var out []string
	fenced := false
	for _, line := range lines {
		if isFence(line) {
			fenced = !fenced
			out = append(out, line)
			continue
		}
		if fenced || isTableLine(line) || utf8.RuneCountInString(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, wrapLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

// wrapLine fills one long line greedily; a continuation line keeps the indent and the width of a list marker.
func wrapLine(line string, width int) []string {
	body := strings.TrimLeft(line, " ")
	prefix := line[:len(line)-len(body)]
	if m := listMarker(body); m != "" {
		prefix += m
		body = body[len(m):]
	}
	indent := strings.Repeat(" ", utf8.RuneCountInString(prefix))
	words := splitWords(body)
	var out []string
	cur, curLen := prefix, utf8.RuneCountInString(prefix)
	empty := true
	for _, w := range words {
		wl := utf8.RuneCountInString(w)
		if !empty && curLen+1+wl > width {
			out = append(out, cur)
			cur, curLen, empty = indent, len(indent), true
		}
		if !empty {
			cur += " "
			curLen++
		}
		cur += w
		curLen += wl
		empty = false
	}
	return append(out, cur)
}

// listMarker returns the leading "- ", "* " or "1. " marker of a line, or "".
func listMarker(s string) string {
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") {
		return s[:2]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && strings.HasPrefix(s[i:], ". ") {
		return s[:i+2]
	}
	return ""
}

// splitWords splits a line at spaces that sit outside code spans.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	for _, seg := range splitSpans(s) {
		if seg.isSpan {
			cur.WriteString(seg.text)
			continue
		}
		for _, r := range seg.text {
			if r == ' ' {
				if cur.Len() > 0 {
					words = append(words, cur.String())
					cur.Reset()
				}
				continue
			}
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// colorize makes line one bold and every code span dim; removing the escape codes returns the text unchanged.
func colorize(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		bold := i == 0 && line != ""
		var b strings.Builder
		if bold {
			b.WriteString(boldOn)
		}
		for _, seg := range splitSpans(line) {
			if !seg.isSpan {
				b.WriteString(seg.text)
				continue
			}
			b.WriteString(dimOn + seg.text + styleOff)
			if bold {
				b.WriteString(boldOn)
			}
		}
		if bold {
			b.WriteString(styleOff)
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// verdictOf returns line one of text with code-span fences and bold markers removed.
func verdictOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	var b strings.Builder
	for _, seg := range splitSpans(line) {
		if seg.isSpan {
			b.WriteString(unspan(seg.text))
		} else {
			b.WriteString(strings.ReplaceAll(seg.text, "**", ""))
		}
	}
	return b.String()
}
