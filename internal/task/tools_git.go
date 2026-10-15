package task

import (
	"context"
	"encoding/json"
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

// hideSecretDiffs keeps the header of each file section of a git patch and drops the body of a secret file's section.
func hideSecretDiffs(out string, g *Guard) string {
	if !strings.Contains(out, "diff --git ") {
		return out
	}
	var b strings.Builder
	hide := false
	for _, ln := range strings.SplitAfter(out, "\n") {
		if rest, ok := strings.CutPrefix(ln, "diff --git "); ok {
			hide = false
			for _, f := range strings.Fields(rest) {
				if g.IsSecretFile(strings.TrimPrefix(strings.TrimPrefix(f, "a/"), "b/")) {
					hide = true
				}
			}
			b.WriteString(ln)
			if hide {
				b.WriteString(secretNote + "\n")
			}
			continue
		}
		if !hide {
			b.WriteString(ln)
		}
	}
	return b.String()
}
