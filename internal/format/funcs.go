package format

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"
)

// Limits on a rendered table and on a path.
const (
	maxRows  = 5  // rows a table shows before the overflow line
	maxCell  = 45 // characters a cell holds before it moves below the table
	maxPath  = 45 // characters a path holds before its middle folders fold
	ellipsis = "…"
)

// funcs is the added template function map; no other added function is callable.
var funcs = template.FuncMap{
	"code":   fnCode,
	"fenced": fnFenced,
	"n":      fnN,
	"plural": fnPlural,
	"dur":    fnDur,
	"path":   fnPath,
	"table":  tableText,
}

// fnCode returns a code word, or a code span holding one, as a code span; a word on neither list fails.
func fnCode(c string) (string, error) {
	word := unspan(c)
	if !IsCode(word) && !IsBlockReason(word) {
		return "", fmt.Errorf("%w: %q", ErrCodeUnknown, word)
	}
	return span(word), nil
}

// fnFenced returns an escaped fact as plain text for a fenced block, breaking every run of 3 or more backticks.
func fnFenced(s string) string {
	raw := unspan(s)
	var b strings.Builder
	run := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '`' {
			if run++; run == 3 {
				b.WriteByte(' ')
				run = 1
			}
		} else {
			run = 0
		}
		b.WriteByte(raw[i])
	}
	return b.String()
}

// fnN returns an integer with comma thousands separators.
func fnN(v any) (string, error) {
	i, err := toInt(v)
	if err != nil {
		return "", err
	}
	s := strconv.FormatInt(i, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for n := len(s) - 3; n > 0; n -= 3 {
		s = s[:n] + "," + s[n:]
	}
	if neg {
		s = "-" + s
	}
	return s, nil
}

// fnPlural returns one when i is 1, else many.
func fnPlural(v any, one, many string) (string, error) {
	i, err := toInt(v)
	if err != nil {
		return "", err
	}
	if i == 1 {
		return one, nil
	}
	return many, nil
}

// toInt reads any signed or unsigned integer value as an int64.
func toInt(v any) (int64, error) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), nil
	}
	return 0, fmt.Errorf("want an integer, got %T", v)
}

// fnDur returns the two largest non-zero units of a duration in hours, minutes and seconds, such as 1h 5m or 42s.
func fnDur(d time.Duration) string {
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	h, m, s := int64(d/time.Hour), int64(d%time.Hour/time.Minute), int64(d%time.Minute/time.Second)
	var out string
	switch {
	case h > 0:
		out = fmt.Sprintf("%dh", h)
		if m > 0 {
			out += fmt.Sprintf(" %dm", m)
		}
	case m > 0:
		out = fmt.Sprintf("%dm", m)
		if s > 0 {
			out += fmt.Sprintf(" %ds", s)
		}
	default:
		out = fmt.Sprintf("%ds", s)
	}
	if out == "0s" {
		return out
	}
	return sign + out
}

// fnPath returns a slash path as a code span; over 45 characters the middle folders fold into an ellipsis.
func fnPath(p string) string {
	raw := unspan(p)
	if utf8.RuneCountInString(raw) <= maxPath {
		return span(raw)
	}
	lead := ""
	rest := raw
	if strings.HasPrefix(rest, "/") {
		lead, rest = "/", rest[1:]
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 3 {
		return span(raw)
	}
	head := lead + segs[0] + "/" + ellipsis + "/"
	folded := head + segs[len(segs)-1]
	for k := len(segs) - 2; k >= 1; k-- {
		cand := head + strings.Join(segs[len(segs)-k:], "/")
		if utf8.RuneCountInString(cand) <= maxPath {
			folded = cand
			break
		}
	}
	return span(folded)
}

// escapedTable returns a copy of t with every string in code-span form.
func escapedTable(t Table) Table {
	out := Table{What: span(t.What)}
	for _, h := range t.Head {
		out.Head = append(out.Head, span(h))
	}
	for _, r := range t.Rows {
		row := make([]string, len(r))
		for i, c := range r {
			row[i] = span(c)
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// tableText renders an escaped Table as Markdown: at most 5 rows, long cells moved below, then the overflow line.
func tableText(t Table) (string, error) {
	cols := len(t.Head)
	if cols == 0 {
		for _, r := range t.Rows {
			cols = max(cols, len(r))
		}
	}
	if cols == 0 {
		return "", nil
	}
	for i, r := range t.Rows {
		if len(t.Head) > 0 && len(r) > len(t.Head) {
			return "", fmt.Errorf("table row %d has %d cells but the header has %d", i+1, len(r), len(t.Head))
		}
	}
	shown := t.Rows[:min(len(t.Rows), maxRows)]
	var lines, below []string
	lines = append(lines, tableRow(t.Head, cols), "|"+strings.Repeat("---|", cols))
	for ri, r := range shown {
		cells := make([]string, cols)
		for ci := range cells {
			if ci >= len(r) {
				continue
			}
			cells[ci] = r[ci]
			if utf8.RuneCountInString(unspan(r[ci])) > maxCell {
				label := ""
				if ci < len(t.Head) {
					label = t.Head[ci]
				}
				below = append(below, fmt.Sprintf("%s, row %d: %s", label, ri+1, r[ci]))
				cells[ci] = "see below"
			}
		}
		lines = append(lines, tableRow(cells, cols))
	}
	out := strings.Join(lines, "\n")
	extra := below
	if more := len(t.Rows) - len(shown); more > 0 {
		line := fmt.Sprintf("%d more", more)
		if what := unspan(t.What); what != "" {
			line += ": " + what
		}
		extra = append(extra, line)
	}
	if len(extra) > 0 {
		out += "\n\n" + strings.Join(extra, "\n")
	}
	return out, nil
}

// tableRow joins cells into one table line, escaping the pipe character inside a cell.
func tableRow(cells []string, cols int) string {
	var b strings.Builder
	b.WriteString("|")
	for i := 0; i < cols; i++ {
		c := ""
		if i < len(cells) {
			c = strings.ReplaceAll(cells[i], "|", `\|`)
		}
		b.WriteString(" " + c + " |")
	}
	return b.String()
}
