package task

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// searchHit is one matching line.
type searchHit struct {
	path string
	line int
	text string
}

// File tool limits.
const (
	editContext  = 3 // lines shown around an edit
	closestLines = 5 // lines shown when an edit's old text is not found
)

// toolRead returns numbered lines of a file.
func toolRead(_ context.Context, tc *toolCall, raw json.RawMessage) Reply {
	const readLines = 2000 // lines one read call returns

	var a readArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}

	r, e := locate(tc, a.Path)
	if e != nil {
		return *e
	}

	root, e := openRoot(r)
	if e != nil {
		return *e
	}

	defer func() { _ = root.Close() }()

	f, err := root.Open(filepath.FromSlash(r.Rel))
	if err != nil {
		return errReply("open %s: %v", r.Rel, cause(err))
	}

	defer func() { _ = f.Close() }()

	if st, err := f.Stat(); err != nil || st.IsDir() {
		return errReply("%s is a folder, not a file", r.Rel)
	}

	br := bufio.NewReader(f)
	if head, _ := br.Peek(8192); isBinary(head) {
		return errReply("%s is a binary file", r.Rel)
	}

	start := max(a.Start, 1)

	end, limited := a.End, false
	if end < start || end-start+1 > readLines {
		end, limited = start+readLines-1, true
	}

	var out strings.Builder

	for n := 1; ; n++ {
		line, err := br.ReadString('\n')
		if line == "" && err != nil {
			if n <= start && start > 1 {
				return errReply("%s has fewer than %d lines", r.Rel, start)
			}
			break
		}

		if n >= start {
			if n > end {
				if limited {
					fmt.Fprintf(&out, "[more lines follow; call read with start=%d]\n", n)
				}
				break
			}

			fmt.Fprintf(
				&out,
				"%d\t%s\n",
				n,
				strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"),
			)
		}

		if err != nil {
			break
		}
	}

	return Reply{Text: out.String()}
}

// bad builds the reply for arguments that cannot be read.
func bad(err error) Reply { return errReply("the arguments cannot be read: %v", err) }

// errReply builds the reply for a failed tool.
func errReply(format string, a ...any) Reply {
	return Reply{Text: "error: " + fmt.Sprintf(format, a...)}
}

// locate returns the path the guard resolved for this call; a path the guard did not resolve is an error, never resolved
// again.
func locate(tc *toolCall, p string) (resolved, *Reply) {
	r, ok := tc.res[p]
	if !ok {
		e := errReply("%s was not checked by the guard", p)
		return resolved{}, &e
	}
	if tc.env.afterResolve != nil {
		tc.env.afterResolve()
	}
	return r, nil
}

// openRoot opens the worktree as a root that refuses any path leaving it, links included.
func openRoot(r resolved) (*os.Root, *Reply) {
	root, err := os.OpenRoot(r.Root)
	if err != nil {
		e := errReply("open the worktree: %v", cause(err))
		return nil, &e
	}
	return root, nil
}

// cause returns the error inside a path error, which names the failure without repeating the path.
func cause(err error) error {
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}

// isBinary reports whether the first bytes of a file hold a NUL.
func isBinary(b []byte) bool { return bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 }

// toolSearch finds lines matching a regular expression, skipping secret files, links and binaries.
func toolSearch(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	const maxSearchLines = 2000 // matches search returns

	var a searchArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}

	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return errReply("the pattern is not a Go regular expression: %v", err)
	}

	r, e := locate(tc, cmpOr(a.Path, "."))
	if e != nil {
		return *e
	}

	root, e := openRoot(r)
	if e != nil {
		return *e
	}

	defer func() { _ = root.Close() }()

	var hits []searchHit

	walk := func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || tc.guard.IsSecretFile(p) {
			return nil
		}

		hits = append(hits, searchFile(root, p, re)...)
		return nil
	}
	if st, err := root.Stat(filepath.FromSlash(r.Rel)); err != nil {
		return errReply("%s does not exist", r.Rel)
	} else if !st.IsDir() {
		_ = walk(r.Rel, fs.FileInfoToDirEntry(st), nil)
	} else {
		_ = fs.WalkDir(root.FS(), r.Rel, walk)
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})

	var out strings.Builder

	for i, h := range hits {
		if i == maxSearchLines {
			fmt.Fprintf(
				&out,
				"[%d more matches not shown; narrow the pattern or the path]\n",
				len(hits)-i,
			)
			break
		}
		fmt.Fprintf(&out, "%s:%d: %s\n", h.path, h.line, h.text)
	}

	if out.Len() == 0 {
		return Reply{Text: "no matches"}
	}

	return Reply{Text: out.String()}
}

// searchFile returns the matching lines of one text file.
func searchFile(root *os.Root, rel string, re *regexp.Regexp) []searchHit {
	const maxSearchFile = 2 << 20 // largest file search looks into

	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	if st, err := f.Stat(); err != nil || st.Size() > maxSearchFile {
		return nil
	}

	br := bufio.NewReader(f)
	if head, _ := br.Peek(8192); isBinary(head) {
		return nil
	}

	var hits []searchHit

	for n := 1; ; n++ {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if re.MatchString(line) {
				hits = append(hits, searchHit{rel, n, clip(line, 300)})
			}
		}
		if err != nil {
			return hits
		}
	}
}

// clip shortens s to n bytes at a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// isRuneStart reports whether b begins a UTF-8 character.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// toolEdit replaces one exact piece of text in a file and formats the file.
func toolEdit(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	const maxFileBytes = 10 << 20 // largest file edit reads

	var a editArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}

	r, e := locate(tc, a.Path)
	if e != nil {
		return *e
	}

	root, e := openRoot(r)
	if e != nil {
		return *e
	}

	defer func() { _ = root.Close() }()

	rel := filepath.FromSlash(r.Rel)
	st, err := root.Stat(rel)
	switch {
	case err != nil:
		return errReply("%s does not exist; use write to create it", r.Rel)
	case !st.Mode().IsRegular() || st.Size() > maxFileBytes:
		return errReply("%s is not a regular file of at most %d MB", r.Rel, maxFileBytes>>20)
	case a.Old == "":
		return errReply("old is empty; give the exact text to replace")
	}

	data, err := root.ReadFile(rel)
	if err != nil {
		return errReply("read %s: %v", r.Rel, cause(err))
	}

	text := string(data)
	switch n := strings.Count(text, a.Old); {
	case n == 0:
		return errReply(
			"old text not found in %s. The closest lines are:\n%s",
			r.Rel,
			closest(text, a.Old),
		)
	case n > 1:
		return errReply(
			"old text occurs %d times in %s; add surrounding lines so it occurs once",
			n,
			r.Rel,
		)
	}

	at := strings.Index(text, a.Old)
	edited := text[:at] + a.New + text[at+len(a.Old):]
	if err := writeAtomicIn(root, rel, []byte(edited), st.Mode().Perm()); err != nil {
		return errReply("write %s: %v", r.Rel, cause(err))
	}

	note := formatFile(ctx, tc, r.Rel)

	final := edited
	if b, err := root.ReadFile(rel); err == nil {
		final = string(b)
	}

	first := strings.Count(edited[:at], "\n")
	if i := strings.Index(final, a.New); i >= 0 && a.New != "" {
		first = strings.Count(final[:i], "\n")
	}

	span := strings.Count(a.New, "\n")
	return Reply{Text: r.Rel + "\n" + lineWindow(final, first, first+span) + note}
}

// closest lists the file lines most like the first line of the missing text.
func closest(text, old string) string {
	want := strings.TrimSpace(strings.SplitN(old, "\n", 2)[0])

	type scored struct {
		n     int
		line  string
		score float64
	}

	var all []scored
	for i, l := range strings.Split(text, "\n") {
		if s := dice(want, strings.TrimSpace(l)); s > 0 {
			all = append(all, scored{i + 1, l, s})
		}
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })

	var b strings.Builder
	for _, s := range all[:min(len(all), closestLines)] {
		fmt.Fprintf(&b, "%d\t%s\n", s.n, clip(s.line, 200))
	}
	if b.Len() == 0 {
		return "(no similar line)"
	}

	return b.String()
}

// dice returns the share of character pairs two strings have in common.
func dice(a, b string) float64 {
	if len(a) < 2 || len(b) < 2 {
		if a == b && a != "" {
			return 1
		}
		return 0
	}

	pairs := map[string]int{}
	for i := 0; i+1 < len(a); i++ {
		pairs[a[i:i+2]]++
	}

	common := 0
	for i := 0; i+1 < len(b); i++ {
		if pairs[b[i:i+2]] > 0 {
			pairs[b[i:i+2]]--
			common++
		}
	}

	return 2 * float64(common) / float64(len(a)+len(b)-2)
}

// writeAtomicIn replaces a file inside the root with data so a reader sees the old file or the new one, never a partial one.
func writeAtomicIn(root *os.Root, rel string, data []byte, perm fs.FileMode) error {
	tmp := filepath.Join(
		filepath.Dir(rel),
		fmt.Sprintf(".%s.tmp-%d", filepath.Base(rel), time.Now().UnixNano()),
	)

	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}

	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		_ = root.Remove(tmp)
	}

	return err
}

// formatFile runs the repository's format command on one file and returns a note when it fails.
func formatFile(ctx context.Context, tc *toolCall, rel string) string {
	if len(tc.env.Format) == 0 || tc.env.Confiner == nil {
		return ""
	}

	arg := rel
	if strings.HasPrefix(arg, "-") {
		arg = "./" + arg
	}

	argv := append(append([]string{}, tc.env.Format...), arg)
	res := confinedRun(ctx, tc, argv, filepath.Join(tc.env.Dir, "out", tc.id+".fmt.log"))
	switch {
	case res.StartErr != nil:
		return "\nnot formatted: " + res.StartErr.Error() + "\n"
	case res.Exit != 0 || res.TimedOut:
		return "\nnot formatted: the format command failed:\n" + res.Reply.Text + "\n"
	}

	return ""
}

// lineWindow shows lines from..to (0-based, inclusive) with editContext lines either side, numbered from 1.
func lineWindow(text string, from, to int) string {
	lines := strings.Split(text, "\n")
	lo, hi := max(from-editContext, 0), min(to+editContext, len(lines)-1)
	var b strings.Builder
	for i := lo; i <= hi; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	return b.String()
}

// toolWrite creates a new file; an existing file is refused.
func toolWrite(_ context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a writeArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}

	r, e := locate(tc, a.Path)
	if e != nil {
		return *e
	}

	root, e := openRoot(r)
	if e != nil {
		return *e
	}

	defer func() { _ = root.Close() }()

	rel := filepath.FromSlash(r.Rel)
	if _, err := root.Lstat(rel); err == nil {
		return errReply("%s already exists; use edit to change it", r.Rel)
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return errReply("create the folder of %s: %v", r.Rel, cause(err))
	}

	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return errReply("%s already exists; use edit to change it", r.Rel)
	}
	if err != nil {
		return errReply("create %s: %v", r.Rel, cause(err))
	}

	_, werr := io.WriteString(f, a.Content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return errReply("write %s: %v", r.Rel, werr)
	}

	return Reply{Text: "created " + r.Rel + ", " + strconv.Itoa(countLines(a.Content)) + " lines"}
}

// countLines counts the lines of a text; a last line without a newline counts.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
