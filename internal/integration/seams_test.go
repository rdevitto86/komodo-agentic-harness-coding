//go:build !windows

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/contextmgr"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// The adapters in this file stand in for pieces the Task Manager provides in production.

// dirTree is a contextmgr.Tree over a folder on disk; the Task Manager serves the base commit instead.
type dirTree struct {
	root  string
	paths []string
	hash  string
}

// newDirTree lists every file under root except .git, in byte order, and fingerprints their paths and bytes.
func newDirTree(t *testing.T, root string) dirTree {
	t.Helper()
	d := dirTree{root: root}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return fs.SkipDir
		}
		if !e.IsDir() {
			rel, _ := filepath.Rel(root, p)
			d.paths = append(d.paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(d.paths)
	h := sha256.New()
	for _, p := range d.paths {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		_, _ = io.WriteString(h, p+"\x00")
		_, _ = h.Write(b)
		_, _ = io.WriteString(h, "\x00")
	}
	d.hash = hex.EncodeToString(h.Sum(nil))
	return d
}

// Hash returns the fingerprint of every path and its bytes.
func (d dirTree) Hash() string { return d.hash }

// Paths returns the file paths in byte order.
func (d dirTree) Paths() []string { return slices.Clone(d.paths) }

// ReadFile returns one file's bytes.
func (d dirTree) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.root, filepath.FromSlash(p)))
}

// Exists reports whether some path matches the glob; a pattern without a slash matches any path segment.
func (d dirTree) Exists(pattern string) bool {
	pattern = strings.TrimPrefix(pattern, "**/")
	for _, p := range d.paths {
		segs := strings.Split(p, "/")
		for i := range segs {
			cand := strings.Join(segs[i:], "/")
			if !strings.Contains(pattern, "/") {
				cand = segs[i]
			}
			if ok, _ := path.Match(pattern, cand); ok {
				return true
			}
		}
	}
	return false
}

// roleGate is the llm.Gate the Task Manager builds from the role's own result check.
type roleGate struct{}

// Check passes a result that ParseBuilderResult accepts and returns the refusal text otherwise.
func (roleGate) Check(_ context.Context, out json.RawMessage) (string, error) {
	if _, err := agent.ParseBuilderResult(out); err != nil {
		return "Your result was refused: " + err.Error() + ". Return the result again.", nil
	}
	return "", nil
}

// recordHost is the llm.ToolHost double: it records each call and serves read from the tree.
type recordHost struct {
	names []string
	tree  dirTree
	mu    sync.Mutex
	calls []string
}

// Advertised returns the tool names the role lists.
func (h *recordHost) Advertised() []string { return h.names }

// Dispatch records the call and answers a read with the file's text.
func (h *recordHost) Dispatch(_ context.Context, name string, args json.RawMessage) llm.ToolReply {
	h.mu.Lock()
	h.calls = append(h.calls, name+" "+string(args))
	h.mu.Unlock()
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	b, err := h.tree.ReadFile(a.Path)
	if name != "read" || err != nil {
		return llm.ToolReply{Text: "error: cannot run " + name}
	}
	return llm.ToolReply{Text: string(b)}
}

// calledWith returns the recorded calls.
func (h *recordHost) calledWith() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

// itemStates turns one role's skill lists into the starting state of the skill table.
func itemStates(t *testing.T, role string, sk config.Skills) []contextmgr.ItemState {
	t.Helper()
	raw, err := fs.ReadFile(komodo.Embedded, "embed/skills/index.json")
	if err != nil {
		t.Fatalf("read skill index: %v", err)
	}
	var idx contextmgr.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("decode skill index: %v", err)
	}
	var out []contextmgr.ItemState
	for _, e := range idx.Skills {
		st := contextmgr.ItemState{Name: e.Name, Origin: contextmgr.OriginEmbedded, Allow: []string{}, Always: []string{}}
		for _, pat := range sk.Allow {
			if ok, _ := path.Match(pat, string(e.Name)); ok {
				st.Enabled, st.Allow = true, []string{role}
			}
		}
		if slices.Contains(sk.Always, string(e.Name)) {
			st.Always = []string{role}
		}
		out = append(out, st)
	}
	return out
}

// knownFor builds the name tables the Configuration Manager checks role files against, from the compiled roles.
func knownFor() config.Known {
	k := config.Known{GuardRows: map[string]bool{}, Tools: map[string]bool{}}
	for _, r := range agent.Roles() {
		for _, g := range r.Guard {
			k.GuardRows[g] = true
		}
		for _, tool := range r.Tools {
			k.Tools[tool] = true
		}
	}
	return k
}
