package agent

import (
	"fmt"
	"path"
	"strings"
)

const (
	// ScopeRequestsPerTask is the number of scope requests one task may make.
	ScopeRequestsPerTask = 1
	// ReasonOneScopeRequest is the denial reason for a second scope request in one task.
	ReasonOneScopeRequest = "one scope request per task"
)

// ScopeRequest is a Builder's request for paths outside its file list.
type ScopeRequest struct {
	Group  string
	Task   string
	Paths  []string
	Reason string
}

// ScopeAnswer is the orchestrator's or the Runtime Manager's answer.
type ScopeAnswer struct {
	Approve bool
	Paths   []string
	Reason  string
}

// OutOfScope returns the changed paths that are neither in the file list nor a test sibling.
// A nil sibling or match function never matches; a file list entry also matches its own path exactly.
func OutOfScope(
	changed, files []string,
	sibling func(path string) bool,
	match func(glob, path string) bool,
) []string {
	var out []string
	for _, c := range changed {
		if inList(c, files, match) || (sibling != nil && sibling(c)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// inList reports whether p equals a file list entry or matches one as a pattern.
func inList(p string, files []string, match func(glob, path string) bool) bool {
	for _, f := range files {
		if f == p || (match != nil && match(f, p)) {
			return true
		}
	}
	return false
}

// AutoDeny returns the first requested path denied without asking anyone, and why.
// A nil callback denies nothing.
func AutoDeny(
	paths []string,
	protected func(string) bool,
	ownedByOther func(string) (group string, ok bool),
) (path, why string, denied bool) {
	for _, p := range paths {
		if protected != nil && protected(p) {
			return p, "protected path", true
		}
		if ownedByOther != nil {
			if g, ok := ownedByOther(p); ok {
				return p, fmt.Sprintf("in the file list of group %s", g), true
			}
		}
	}
	return "", "", false
}

// MatchGlob matches a slash path against a guard pattern: * within one segment, ** across segments, ? one character, [abc] a
// set.
// A pattern with no slash matches the base name at any depth; a group's file list uses MatchPath instead.
func MatchGlob(glob, p string) bool {
	if !strings.Contains(glob, "/") {
		ok, err := path.Match(glob, path.Base(p))
		return err == nil && ok
	}
	return matchSegments(strings.Split(glob, "/"), strings.Split(p, "/"))
}

// matchSegments matches pattern segments against path segments, letting ** take zero or more whole segments.
func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// MatchPath matches a slash path against a file list entry: the same pattern grammar as MatchGlob,
// but an entry with no slash matches only at the root.
func MatchPath(glob, p string) bool {
	return matchSegments(strings.Split(glob, "/"), strings.Split(p, "/"))
}
