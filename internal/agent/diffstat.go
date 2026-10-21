package agent

import "strings"

// DiffFiles reads the changed files of a unified diff with their added and removed line counts, in diff order.
func DiffFiles(diff string) []ChangedFile {
	var (
		out    []ChangedFile
		oldP   string
		inHunk bool
	)

	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inHunk = false
		case !inHunk && strings.HasPrefix(l, "--- "):
			oldP = strings.TrimPrefix(strings.TrimPrefix(l, "--- "), "a/")
		case !inHunk && strings.HasPrefix(l, "+++ "):
			p := strings.TrimPrefix(l, "+++ ")
			if p == "/dev/null" {
				p = oldP
			}

			out = append(out, ChangedFile{Path: strings.TrimPrefix(p, "b/")})
		case strings.HasPrefix(l, "@@"):
			inHunk = true
		case inHunk && len(out) > 0 && strings.HasPrefix(l, "+"):
			out[len(out)-1].Added++
		case inHunk && len(out) > 0 && strings.HasPrefix(l, "-"):
			out[len(out)-1].Removed++
		}
	}

	return out
}
