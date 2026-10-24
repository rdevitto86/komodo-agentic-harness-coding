package intake

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
)

// readDocs reads each document a plan request names from the base commit and renders them as one specs slot.
func readDocs(tree contextmgr.Tree, docs []string) (string, error) {
	var b strings.Builder

	for _, d := range docs {
		if d == "" || filepath.IsAbs(d) || slices.Contains(strings.Split(d, "/"), "..") {
			return "", fmt.Errorf("document %q is not a path inside the repository", d)
		}

		body, err := tree.ReadFile(d)
		if err != nil {
			return "", fmt.Errorf("read document %s: %w", d, err)
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}

		fmt.Fprintf(&b, "## %s\n\n%s", d, strings.TrimSpace(string(body)))
	}

	return b.String(), nil
}
