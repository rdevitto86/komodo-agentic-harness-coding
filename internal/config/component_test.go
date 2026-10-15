package config

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const modulePath = "github.com/rdevitto86/komodo-agentic-harness-coding"

func TestHandEditTripsTheGuard(t *testing.T) {
	m, home := newTestManager(t)
	if err := m.Verify(); err != nil {
		t.Fatalf("Defaults state: %v", err)
	}
	mustSet(t, m, "limits.returns", 4)
	if err := m.Verify(); err != nil {
		t.Fatalf("Guarded state: %v", err)
	}
	writeFile(t, home, "settings.json", `{"limits.returns": 2}`)
	if err := m.Verify(); !errors.Is(err, ErrTampered) {
		t.Fatalf("want ErrTampered, got %v", err)
	}
	it, err := m.GetConfigItem("limits.returns")
	if err != nil || it.Value != 2 {
		t.Fatalf("reads must keep working: %+v %v", it, err)
	}
	if _, err := m.SetConfigItem(Change{Key: "limits.returns", New: 1}); !errors.Is(err, ErrTampered) {
		t.Fatalf("a write must be refused: %v", err)
	}
	before := len(logRows(t, home))
	b, err := m.Accept()
	if err != nil || len(b.Changes) != 1 || b.Changes[0].Key != "settings.json" {
		t.Fatalf("Accept: %+v %v", b, err)
	}
	if err := m.Verify(); err != nil {
		t.Fatalf("after Accept: %v", err)
	}
	rows := logRows(t, home)
	if len(rows) != before+1 || rows[len(rows)-1]["key"] != "settings.json" {
		t.Fatalf("want one acceptance row, got %v", rows[before:])
	}
	if b, _ := m.Accept(); len(b.Changes) != 0 {
		t.Fatal("a second Accept logged rows")
	}
}

func TestTamperStates(t *testing.T) {
	m, home := newTestManager(t)
	// A machine file with no checksum is Tampered.
	writeFile(t, home, "agents.json", `{}`)
	if err := m.Verify(); !errors.Is(err, ErrTampered) {
		t.Fatalf("file without checksum: %v", err)
	}
	// Accept with no machine files returns to Defaults and deletes the checksum.
	if err := os.Remove(filepath.Join(home, "agents.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, home, "state/config.sha", `{"settings":"","agents":""}`)
	b, err := m.Accept()
	if err != nil || len(b.Changes) != 0 {
		t.Fatalf("Accept with no files: %+v %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "config.sha")); !os.IsNotExist(err) {
		t.Fatal("config.sha was not deleted")
	}
	if err := m.Verify(); err != nil {
		t.Fatalf("Defaults after Accept: %v", err)
	}
	// A checksum that names a file that is gone is Tampered.
	mustSet(t, m, "limits.returns", 4)
	if err := os.Remove(filepath.Join(home, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(); !errors.Is(err, ErrTampered) {
		t.Fatalf("checksum without file: %v", err)
	}
}

// TestCrashHelper is the child process of TestCrashPoints; it dies at the stage in KOMODO_CRASH_AT.
func TestCrashHelper(t *testing.T) {
	stage := os.Getenv("KOMODO_CRASH_AT")
	if stage == "" {
		t.Skip("child of TestCrashPoints")
	}
	m, err := New(os.Getenv("KOMODO_HOME"), Known{}, probeAll)
	if err != nil {
		t.Fatal(err)
	}
	crashHook = func(s string) {
		if s == stage {
			os.Exit(7)
		}
	}
	_, _ = m.SetConfigItem(Change{Key: "limits.returns", New: 4})
	t.Fatal("the crash hook did not fire")
}

// crashAt runs a write in a child process that is killed at stage and returns the home folder.
func crashAt(t *testing.T, stage string) (*Manager, string) {
	t.Helper()
	m, home := newTestManager(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	cmd.Env = append(os.Environ(), "KOMODO_CRASH_AT="+stage, "KOMODO_HOME="+home)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("child did not die at %s: %v", stage, err)
	}
	return m, home
}

func TestCrashPoints(t *testing.T) {
	t.Run("after the audit rows, before the file", func(t *testing.T) {
		m, home := crashAt(t, "after_audit")
		if rows := logRows(t, home); len(rows) != 1 || rows[0]["key"] != "limits.returns" {
			t.Fatalf("log %v", rows)
		}
		if readFile(t, home, "settings.json") != "" {
			t.Fatal("the file was written")
		}
		if it, _ := m.GetConfigItem("limits.returns"); it.Value != 3 {
			t.Fatalf("settings as before: %+v", it)
		}
		if err := m.Verify(); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		// The dead writer left its lock; once stale, the next write replaces it.
		lock := filepath.Join(home, "state", "settings.lock")
		old := time.Now().Add(-time.Minute)
		if err := os.Chtimes(lock, old, old); err != nil {
			t.Fatalf("lock left behind? %v", err)
		}
		mustSet(t, m, "limits.returns", 5)
		rows := logRows(t, home)
		if len(rows) != 2 || rows[1]["old"].(float64) != 3 {
			t.Fatalf("the next row's old value should show the batch never applied: %v", rows)
		}
	})
	t.Run("after the file, before the checksum", func(t *testing.T) {
		m, home := crashAt(t, "after_files")
		stale := time.Now().Add(-time.Minute)
		if err := os.Chtimes(filepath.Join(home, "state", "settings.lock"), stale, stale); err != nil {
			t.Fatal(err)
		}
		if it, _ := m.GetConfigItem("limits.returns"); it.Value != 4 {
			t.Fatalf("settings as written: %+v", it)
		}
		if err := m.Verify(); !errors.Is(err, ErrTampered) {
			t.Fatalf("want Tampered, got %v", err)
		}
		if _, err := m.Accept(); err != nil {
			t.Fatal(err)
		}
		if err := m.Verify(); err != nil {
			t.Fatalf("after Accept: %v", err)
		}
		if len(logRows(t, home)) != 2 {
			t.Fatalf("want the batch row and one acceptance row, got %v", logRows(t, home))
		}
	})
}

func TestWritersNeverInterleave(t *testing.T) {
	_, home := newTestManager(t)
	pairs := [][2]string{
		{"limits.returns", "limits.group_attempts"}, {"cache.max_mb", "bus.buffer_events"},
		{"metrics.retention_days", "check.coverage"}, {"lease.lock_timeout_s", "warn.quota_pct"},
		{"guard.refusal_limit", "limits.pause_max_min"}, {"sandbox.max_procs", "sandbox.lang_cache_gb"},
	}
	var wg sync.WaitGroup
	ids := make([]string, len(pairs))
	for i, p := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := New(home, Known{}, probeAll)
			if err != nil {
				t.Error(err)
				return
			}
			b, err := m.SetConfigItem(Change{Key: p[0], New: 2}, Change{Key: p[1], New: 4})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = b.ID
		}()
	}
	wg.Wait()
	rows := logRows(t, home)
	if len(rows) != 12 {
		t.Fatalf("want 12 rows, got %d", len(rows))
	}
	var order []string
	for i, r := range rows {
		b := r["batch"].(string)
		if i%2 == 1 && rows[i-1]["batch"] != b {
			t.Fatalf("rows interleaved at %d", i)
		}
		if i%2 == 0 {
			order = append(order, b)
		}
	}
	if !slices.IsSorted(order) {
		t.Fatalf("batch ids are not in time order: %v", order)
	}
	m, _ := New(home, Known{}, probeAll)
	if err := m.Verify(); err != nil {
		t.Fatalf("Verify after concurrent writes: %v", err)
	}
	if it, _ := m.GetConfigItem("bus.buffer_events"); it.Value != 4 {
		t.Fatalf("a write was lost: %+v", it)
	}
}

func TestLockWaitAndStale(t *testing.T) {
	m, home := newTestManager(t)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(home, "state", "settings.lock")
	if err := os.WriteFile(lock, []byte("999"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := lockWait
	lockWait = 150 * time.Millisecond
	defer func() { lockWait = old }()
	start := time.Now()
	_, err := m.SetConfigItem(Change{Key: "limits.returns", New: 4})
	if !errors.Is(err, ErrLocked) || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("want ErrLocked after the wait, got %v", err)
	}
	stale := time.Now().Add(-31 * time.Second)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	mustSet(t, m, "limits.returns", 4)
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("the lock was not released")
	}
}

func TestModelCheck(t *testing.T) {
	m, home := newTestManager(t)
	reject, err := New(home, Known{}, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	var le LimitError
	_, err = reject.SetConfigItem(Change{Key: "agents.builder.model", New: "gpt-9"})
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "komodo install --machine-only") {
		t.Fatalf("unprobed model: %v", err)
	}
	if readFile(t, home, "agents.json") != "" {
		t.Fatal("a refused model wrote a file")
	}
	nilProbe, _ := New(home, Known{}, nil)
	if _, err := nilProbe.SetConfigItem(Change{Key: "agents.builder.model", New: "sonnet"}); !errors.As(err, &le) {
		t.Fatalf("no probe record: %v", err)
	}
	mustSet(t, m, "agents.builder.model", "sonnet")
	it, _ := m.GetConfigItem("agents.builder.model")
	if it.Value != "sonnet" || it.Source != LayerMachine {
		t.Fatalf("item %+v", it)
	}
	if !strings.Contains(readFile(t, home, "agents.json"), `"model": "sonnet"`) {
		t.Fatalf("agents.json: %s", readFile(t, home, "agents.json"))
	}
}

func TestLiveModelReachesNextSessionOnly(t *testing.T) {
	m, _ := newTestManager(t)
	mustSet(t, m, "agents.builder.model", "sonnet")
	snap, _ := m.Snapshot()
	running, _ := m.GetConfigItem("agents.builder.model") // a session that started on this model
	mustSet(t, m, "agents.builder.model", "opus")
	next, _ := m.GetConfigItem("agents.builder.model") // the next session start
	if running.Value != "sonnet" || next.Value != "opus" {
		t.Fatalf("running %v next %v", running.Value, next.Value)
	}
	after, _ := m.Snapshot()
	if after.Hash != snap.Hash {
		t.Fatal("the run's pinned settings moved")
	}
	mustSet(t, m, "limits.returns", 4)
	if after2, _ := m.Snapshot(); after2.Hash == snap.Hash || snap.Flat["limits.returns"].Value != 3 {
		t.Fatal("the held snapshot must keep its own copy")
	}
}

func TestOnlyAllowedPackagesImportConfig(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := []string{"internal/cli", "internal/engine", "cmd/komodo", "internal/config"}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".claude" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir, _ := filepath.Rel(root, filepath.Dir(p))
		dir = filepath.ToSlash(dir)
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == modulePath+"/internal/config" && !slices.Contains(allowed, dir) {
				t.Errorf("%s imports internal/config", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfigImportsStayBelowIt(t *testing.T) {
	ok := []string{modulePath, modulePath + "/internal/format", modulePath + "/internal/bus", modulePath + "/internal/core"}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, p := range files {
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(strings.Split(path, "/")[0], ".") && !slices.Contains(ok, path) {
				t.Errorf("%s imports %s", p, path)
			}
		}
	}
}

func TestDiscoveryListsEveryKey(t *testing.T) {
	m, _ := newTestManager(t)
	items := m.ListConfigItems("")
	seen := map[string]Item{}
	for _, it := range items {
		seen[it.Key] = it
		if it.Source != LayerBuiltIn && it.Source != LayerMachine {
			t.Errorf("%s has no source", it.Key)
		}
	}
	for _, s := range m.specs {
		it, ok := seen[s.Name]
		if !ok {
			t.Errorf("table key %s not listed", s.Name)
			continue
		}
		if s.Default != nil && !equalValues(it.Default, s.Default) {
			t.Errorf("%s default %v", s.Name, it.Default)
		}
		if s.Max != nil && !equalValues(it.Limit, s.Max) {
			t.Errorf("%s limit %v", s.Name, it.Limit)
		}
		if s.Kind == KindEnum && !slices.Equal(it.Allowed, s.Enum) {
			t.Errorf("%s allowed %v", s.Name, it.Allowed)
		}
	}
	for _, role := range roleNames {
		for _, f := range []string{"model", "session_wall_min", "session_idle_min", "retries"} {
			if _, ok := seen["agents."+role+"."+f]; !ok {
				t.Errorf("agents.%s.%s not listed", role, f)
			}
		}
	}
	if got := len(m.ListConfigItems("agents.reviewer.")); got != 4 {
		t.Errorf("prefix listing: %d", got)
	}
	if !slices.IsSortedFunc(items, func(a, b Item) int { return strings.Compare(a.Key, b.Key) }) {
		t.Error("items are not sorted")
	}
	if it := seen["limits.returns"]; it.Default != 3 || it.Limit != 5 || it.Source != LayerBuiltIn {
		t.Errorf("limits.returns %+v", it)
	}
}

func TestFamilyKeys(t *testing.T) {
	m, _ := newTestManager(t)
	mustSet(t, m, "limits.link_sessions.claude-cli", 5)
	it, err := m.GetConfigItem("limits.link_sessions.claude-cli")
	if err != nil || it.Value != 5 || it.Default != 3 || it.Limit != 8 {
		t.Fatalf("%+v %v", it, err)
	}
	it, err = m.GetConfigItem("limits.link_sessions.anthropic-api")
	if err != nil || it.Value != 3 || it.Source != LayerBuiltIn {
		t.Fatalf("%+v %v", it, err)
	}
	var le LimitError
	if _, err := m.SetConfigItem(Change{Key: "limits.link_sessions.claude-cli", New: 9}); !errors.As(err, &le) {
		t.Fatalf("family limit: %v", err)
	}
	var uk UnknownKeyError
	if _, err := m.GetConfigItem("limits.link_sessions."); !errors.As(err, &uk) {
		t.Fatalf("empty variable part: %v", err)
	}
}
