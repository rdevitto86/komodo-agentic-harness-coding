package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CardHash returns the card hash of one group: sha256 over TG.md without its status line, then each tsk-*.md in byte order of file name.
func CardHash(root string, g Group) (string, error) {
	var epic, idx int
	if _, err := fmt.Sscanf(g.Name, "tg-%d.%d", &epic, &idx); err != nil {
		return "", fmt.Errorf("group name %q is not tg-N.M", g.Name)
	}
	rel := path.Join(backlogDir, fmt.Sprintf("epic-%d", epic), g.Name)
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read group folder: %w", err)
	}
	names := []string{"TG.md"}
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "tsk-") && strings.HasSuffix(n, ".md") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", fmt.Errorf("read card file: %w", err)
		}
		if n == "TG.md" {
			b = dropStatusLine(b)
		}
		h.Write([]byte(path.Join(rel, n) + "\n" + strconv.Itoa(len(b)) + "\n"))
		h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// dropStatusLine removes the status: line from the field block of a TG.md.
func dropStatusLine(b []byte) []byte {
	var out strings.Builder
	inBlock := true
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if strings.TrimSpace(l) == "" {
			inBlock = false
		}
		if inBlock && strings.HasPrefix(l, "status:") {
			continue
		}
		out.WriteString(l)
	}
	return []byte(out.String())
}
