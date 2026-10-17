package engine

import (
	"regexp"
	"strings"
	"sync"
)

// globCache holds the compiled form of each pattern seen.
var globCache sync.Map

// globMatch reports whether path matches pattern; * stays inside one folder, ** crosses folders and
// ? is one character.
func globMatch(pattern, path string) bool {
	if v, ok := globCache.Load(pattern); ok {
		return v.(*regexp.Regexp).MatchString(path)
	}

	var b strings.Builder
	b.WriteString("^")

	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++

					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}

	globCache.Store(pattern, re)

	return re.MatchString(path)
}

// anyGlob reports whether path matches any pattern.
func anyGlob(patterns []string, path string) bool {
	for _, p := range patterns {
		if globMatch(p, path) {
			return true
		}
	}

	return false
}
