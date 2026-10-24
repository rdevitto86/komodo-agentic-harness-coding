package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

// probeAll accepts every model name.
func probeAll(string) bool { return true }

// newTestManager builds a manager over a temporary home and the real embedded data.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KOMODO_HOME", home)
	m, err := New(home, Known{}, probeAll)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, home
}

// embedCopy copies the embedded data into a map filesystem a test can edit.
func embedCopy(t *testing.T) fstest.MapFS {
	t.Helper()
	sub, err := fs.Sub(komodo.Embedded, "embed")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		out[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// writeFile writes a machine file under home.
func writeFile(t *testing.T, home, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readFile reads a file under home and returns "" when it is absent.
func readFile(t *testing.T, home, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, name))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// logRows decodes the audit log into rows.
func logRows(t *testing.T, home string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, home, "state/config.log")), "\n") {
		if line == "" {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		rows = append(rows, r)
	}
	return rows
}

// set runs SetConfigItem with key/value pairs and fails the test on error.
func mustSet(t *testing.T, m *Manager, kv ...any) Batch {
	t.Helper()
	var cs []Change
	for i := 0; i < len(kv); i += 2 {
		cs = append(cs, Change{Key: kv[i].(string), New: kv[i+1]})
	}
	b, err := m.SetConfigItem(cs...)
	if err != nil {
		t.Fatalf("SetConfigItem: %v", err)
	}
	return b
}
