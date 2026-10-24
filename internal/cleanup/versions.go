package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// UnusedVersions returns the installed binary versions that no unfinished group names and
// that is not the running binary's, oldest first; it fails closed when a version is unknown.
func (h *Handler) UnusedVersions(ctx context.Context) ([]string, error) {
	if h.Groups == nil {
		return nil, errors.New("no group reader to name the versions in use")
	}

	used, err := h.Groups.Unfinished(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unfinished groups: %w", err)
	}
	if h.Current == "" {
		return nil, fmt.Errorf("version of the running binary: %w", ErrUnknownVersion)
	}

	named := map[string]bool{h.Current: true}
	for _, g := range used {
		if g.Version == "" {
			return nil, fmt.Errorf("unfinished group %s: %w", g.Group, ErrUnknownVersion)
		}
		named[g.Version] = true
	}

	ents, err := os.ReadDir(h.Roots.Versions)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list installed versions: %w", err)
	}

	var out []string
	for _, e := range ents {
		if e.Type().IsDir() && !named[e.Name()] {
			out = append(out, e.Name())
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return olderVersion(out[i], out[j]) })
	return out, nil
}

// olderVersion orders two version names by semantic version; a name that does not parse sorts last, by text.
func olderVersion(a, b string) bool {
	va, ea := core.ParseSemVer(a)
	vb, eb := core.ParseSemVer(b)
	switch {
	case ea == nil && eb == nil:
		return va.Compare(vb) < 0
	case ea == nil:
		return true
	case eb == nil:
		return false
	}
	return a < b
}
