package agent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// scopeMode is where a line of a Go file sits, as far as the diff shows.
type scopeMode int

const (
	scopeUnknown    scopeMode = iota // the diff does not show it, so an exported-looking line counts as public
	scopeTop                         // between declarations
	scopeBody                        // inside a function body
	scopeBlock                       // inside a var, const or type block, or an exported struct or interface
	scopeClosed                      // inside an unexported type or var, or an import block
	scopeSigPublic                   // inside the wrapped signature of an exported function or method
	scopeSigPrivate                  // inside the wrapped signature of an unexported function or method
)

// memberLine matches an indented line that starts with an exported name, as a field, method, constant or embedded type.
var memberLine = regexp.MustCompile(`^\t{1,2}\*?(?:[a-z]\w*\.)?\p{Lu}`)

// PublicAPIChanged reports whether a unified diff adds or removes a Go declaration of an exported name outside test files.
// It reads lines only, and errs toward true whenever the diff leaves a line's place unclear.
func PublicAPIChanged(diff string) bool {
	var (
		oldP, newP string
		inHunk     bool
		mode       scopeMode
	)

	// relevant reports whether the current file is a non-test Go file under either of its paths; unknown paths are.
	relevant := func() bool {
		if oldP == "" && newP == "" {
			return true
		}
		return isPublicGoFile(oldP) || isPublicGoFile(newP)
	}

	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			oldP, newP = splitGitHeader(strings.TrimPrefix(l, "diff --git "))
			inHunk = false
		case inHunk && strings.HasPrefix(l, "@@"):
			mode = hunkScope(l)
		case inHunk && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")):
			if relevant() && publicLine(l[1:], mode) {
				return true
			}
			mode = enterScope(l[1:], mode)
		case inHunk && strings.HasPrefix(l, " "):
			mode = enterScope(l[1:], mode)
		case inHunk && l == "":
		case strings.HasPrefix(l, "@@"):
			inHunk, mode = true, hunkScope(l)
		case strings.HasPrefix(l, "rename from "):
			oldP = strings.TrimPrefix(l, "rename from ")
		case strings.HasPrefix(l, "rename to "):
			newP = strings.TrimPrefix(l, "rename to ")
			if oldP != newP && (isPublicGoFile(oldP) || isPublicGoFile(newP)) {
				return true
			}
		case strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch":
			if relevant() {
				return true
			}
		}
	}

	return false
}

// isPublicGoFile reports whether p is a Go file that is not a test file.
func isPublicGoFile(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
}

// hunkScope reads the scope a hunk starts in from the function text git prints after its header.
func hunkScope(header string) scopeMode {
	i := strings.Index(header[2:], "@@")
	if i < 0 {
		return scopeUnknown
	}

	text := strings.TrimSpace(header[i+4:])
	if strings.HasPrefix(text, "func ") {
		return scopeBody
	}

	if m := enterScope(text, scopeUnknown); m == scopeBlock || m == scopeClosed {
		return m
	}

	return scopeUnknown
}

// enterScope returns the scope after a line: a column-zero declaration or closing bracket moves it, anything else keeps it.
func enterScope(c string, cur scopeMode) scopeMode {
	opens := strings.HasSuffix(c, "{") || strings.HasSuffix(c, "(")
	switch {
	case strings.HasPrefix(c, "func "):
		if strings.HasSuffix(c, "{") {
			return scopeBody
		}
		if publicLine(c, scopeTop) {
			return scopeSigPublic
		}
		return scopeSigPrivate
	case cur == scopeSigPublic || cur == scopeSigPrivate:
		if strings.HasPrefix(c, ")") && strings.HasSuffix(c, "{") {
			return scopeBody
		}
		return cur
	case strings.HasPrefix(c, "import ("):
		return scopeClosed
	case strings.HasPrefix(c, "}"), strings.HasPrefix(c, ")"):
		return scopeTop
	case strings.HasPrefix(c, "type ("), strings.HasPrefix(c, "var ("), strings.HasPrefix(c, "const ("):
		return scopeBlock
	case !opens:
		return cur
	}

	for _, kw := range []string{"type ", "var ", "const "} {
		if rest, ok := strings.CutPrefix(c, kw); ok {
			if exportedStart(rest) {
				return scopeBlock
			}
			return scopeClosed
		}
	}

	return cur
}

// publicLine reports whether one added or removed line declares an exported name in the scope it sits in.
func publicLine(c string, mode scopeMode) bool {
	switch {
	case strings.HasPrefix(c, "func "):
		rest := c[len("func "):]
		if strings.HasPrefix(rest, "(") {
			i := strings.Index(rest, ")")
			if i < 0 {
				return true
			}
			rest = strings.TrimSpace(rest[i+1:])
		}
		return exportedStart(rest)
	}

	for _, kw := range []string{"type ", "var ", "const "} {
		if rest, ok := strings.CutPrefix(c, kw); ok {
			return exportedStart(rest)
		}
	}

	if mode == scopeSigPublic {
		return true
	}

	return (mode == scopeBlock || mode == scopeUnknown) && memberLine.MatchString(c)
}

// exportedStart reports whether s begins with an upper-case letter.
func exportedStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}
