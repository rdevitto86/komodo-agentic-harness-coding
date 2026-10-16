package task

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// secretNote replaces the body of a secret file in a diff.
const secretNote = "[the contents of a secret file are not shown]"

// toolDiff returns the group's diff against its base commit with secret files shown by name only.
func toolDiff(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a diffArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if tc.env.Git == nil {
		return errReply("no git is set for this session")
	}
	out, err := tc.env.Git.Diff(ctx, a.Paths)
	if err != nil {
		return errReply("%v", err)
	}
	return Reply{Text: hideSecretDiffs(out, tc.guard)}
}

// toolGitRead runs log, show, blame or status and returns the output with secret files shown by name only.
func toolGitRead(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a gitReadArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if tc.env.Git == nil {
		return errReply("no git is set for this session")
	}
	out, err := tc.env.Git.ReadOnly(ctx, a.Command, a.Args)
	if err != nil {
		return errReply("%v", err)
	}
	return Reply{Text: hideSecretDiffs(out, tc.guard)}
}

// hideSecretDiffs keeps each diff header and drops the body of a section that names a secret file or whose header cannot be read.
// A section ends at the next diff header or at a commit line.
func hideSecretDiffs(out string, g *Guard) string {
	var b strings.Builder
	hide := false
	for _, ln := range strings.SplitAfter(out, "\n") {
		switch {
		case strings.HasPrefix(ln, "commit "):
			hide = false
			b.WriteString(ln)
		case strings.HasPrefix(ln, "diff "):
			hide = headerHasSecret(strings.TrimRight(ln, "\r\n"), g)
			b.WriteString(ln)
			if hide {
				b.WriteString(secretNote + "\n")
			}
		case hide && (ln == "" || ln == "\n" || strings.ContainsRune(" +-@\\", rune(ln[0]))):
		default:
			b.WriteString(ln)
		}
	}
	return b.String()
}

// headerHasSecret reports whether a "diff" header names a secret file; a header it cannot read counts as naming one.
func headerHasSecret(h string, g *Guard) bool {
	var rest string
	for _, p := range []string{"diff --git ", "diff --cc ", "diff --combined "} {
		if r, ok := strings.CutPrefix(h, p); ok {
			rest = r
		}
	}
	if rest == "" {
		return true
	}
	names, ok := headerNames(rest)
	if !ok {
		return true
	}
	for _, n := range names {
		if g.IsSecretFile(strings.TrimPrefix(strings.TrimPrefix(n, "a/"), "b/")) {
			return true
		}
	}
	return false
}

// headerNames splits the paths of a diff header: quoted names are unquoted the way git writes them, and the rest split at spaces.
func headerNames(s string) ([]string, bool) {
	var out []string
	for s = strings.TrimLeft(s, " "); s != ""; s = strings.TrimLeft(s, " ") {
		if s[0] != '"' {
			tok, rest, _ := strings.Cut(s, " ")
			out, s = append(out, tok), rest
			continue
		}
		end := -1
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
			} else if s[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, false
		}
		name, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return nil, false
		}
		out, s = append(out, name), s[end+1:]
	}
	return out, len(out) > 0
}
