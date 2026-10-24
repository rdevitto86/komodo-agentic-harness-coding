package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Signoff is Quality Assurance's accepted pass.
type Signoff struct {
	Tree      string // tree hash of the work under test
	TestsHash string // hash of the test files at that tree
}

// TestsHash hashes the path and content of every test file, in path order.
func TestsHash(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		// Lengths come first so a path and its content cannot trade bytes.
		h.Write([]byte(strconv.Itoa(len(p)) + ":" + p + strconv.Itoa(len(files[p])) + ":"))
		h.Write(files[p])
	}

	return hex.EncodeToString(h.Sum(nil))
}

// Voided reports whether a test file changed since the sign-off, which reopens Quality Assurance.
func (s Signoff) Voided(testsHash string) bool { return s.TestsHash != testsHash }

// IsTestFile reports whether a repository path is test material, by the rule the risk levels use.
func IsTestFile(p string) bool {
	p = strings.ToLower(strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, "\\", "/")), "./"))
	dir, base := path.Split(p)

	return isTestPath(strings.Split(strings.Trim(dir, "/"), "/"), base, path.Ext(base))
}
