package agent

import "strings"

// diffEntry is one `diff --git` block while it is read.
type diffEntry struct {
	oldP, newP       string // the path before and after; empty for an added or a deleted file
	added, removed   int
	binary, hasPatch bool
}

// DiffFiles reads the changed files of a unified diff with their added and removed line counts, in diff order. A renamed
// file is listed under both its paths, a deleted file under its old path, and a binary file is marked Binary.
func DiffFiles(diff string) []ChangedFile {
	var (
		out    []ChangedFile
		cur    *diffEntry
		inHunk bool
	)

	flush := func() {
		if cur != nil {
			out = append(out, cur.files()...)
		}
	}

	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			flush()

			o, n := splitGitHeader(strings.TrimPrefix(l, "diff --git "))
			cur, inHunk = &diffEntry{oldP: o, newP: n}, false
		case cur == nil:
		case inHunk && strings.HasPrefix(l, "+"):
			cur.added++
		case inHunk && strings.HasPrefix(l, "-"):
			cur.removed++
		case inHunk && !strings.HasPrefix(l, "@@"):
		case strings.HasPrefix(l, "@@"):
			inHunk = true
		case strings.HasPrefix(l, "rename from "):
			cur.oldP = strings.TrimPrefix(l, "rename from ")
		case strings.HasPrefix(l, "rename to "):
			cur.newP = strings.TrimPrefix(l, "rename to ")
		case strings.HasPrefix(l, "new file mode "):
			cur.oldP = ""
		case strings.HasPrefix(l, "deleted file mode "):
			cur.newP = ""
		case strings.HasPrefix(l, "--- "):
			if p := strings.TrimPrefix(l, "--- "); p != "/dev/null" {
				cur.oldP = strings.TrimPrefix(p, "a/")
			}
		case strings.HasPrefix(l, "+++ "):
			if p := strings.TrimPrefix(l, "+++ "); p != "/dev/null" {
				cur.newP = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch":
			cur.binary = true
		}
	}

	flush()

	return out
}

// files returns the entry as changed files: the path it ends at carries the counts, and a renamed file's old path follows.
func (e *diffEntry) files() []ChangedFile {
	main := e.newP
	if main == "" {
		main = e.oldP
	}
	if main == "" {
		return nil
	}

	out := []ChangedFile{{Path: main, Added: e.added, Removed: e.removed, Binary: e.binary}}
	if e.oldP != "" && e.oldP != main {
		out = append(out, ChangedFile{Path: e.oldP, Binary: e.binary})
	}

	return out
}

// splitGitHeader reads the old and new path of a `diff --git a/X b/Y` line; a rename line corrects it when the path has a
// space.
func splitGitHeader(s string) (string, string) {
	if n := (len(s) - 5) / 2; len(s) >= 5 && len(s) == 2*n+5 && s[2:2+n] == s[len(s)-n:] {
		return s[2 : 2+n], s[len(s)-n:]
	}

	if i := strings.Index(s, " b/"); i >= 2 {
		return strings.TrimPrefix(s[:i], "a/"), s[i+3:]
	}

	return "", ""
}
