package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictReadingNamesClosestKey(t *testing.T) {
	m, home := newTestManager(t)
	writeFile(t, home, "settings.json", `{"limit.group_retries": 2}`)
	_, err := m.Snapshot()
	var uk UnknownKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("want UnknownKeyError, got %v", err)
	}
	if uk.Key != "limit.group_retries" || uk.Closest != "limits.group_retries" {
		t.Fatalf("got key %q closest %q", uk.Key, uk.Closest)
	}
	if !strings.Contains(err.Error(), "komodo update") {
		t.Fatalf("error lacks the fix: %v", err)
	}
}

func TestStrictReadingRefusesBadTypeAndLimit(t *testing.T) {
	m, home := newTestManager(t)
	writeFile(t, home, "settings.json", `{"limits.group_retries": "three"}`)
	var bv BadValueError
	if _, err := m.Snapshot(); !errors.As(err, &bv) {
		t.Fatalf("want BadValueError, got %v", err)
	}
	writeFile(t, home, "settings.json", `{"limits.group_retries": 9}`)
	var le LimitError
	if _, err := m.Snapshot(); !errors.As(err, &le) || le.Key != "limits.group_retries" {
		t.Fatalf("want LimitError on limits.group_retries, got %v", err)
	}
}

func TestLimitErrorLeavesFileUnchanged(t *testing.T) {
	m, home := newTestManager(t)
	_, err := m.SetConfigItem(Change{Key: "limits.group_retries", New: 9})
	var le LimitError
	if !errors.As(err, &le) {
		t.Fatalf("want LimitError, got %v", err)
	}
	if le.Key != "limits.group_retries" || le.Limit != 5 || le.Value != 9 {
		t.Fatalf("got %+v", le)
	}
	if readFile(t, home, "settings.json") != "" || readFile(t, home, "state/config.log") != "" {
		t.Fatal("a refused write touched the files")
	}
}

func TestSetErrorKinds(t *testing.T) {
	m, _ := newTestManager(t)
	var uk UnknownKeyError
	if _, err := m.SetConfigItem(Change{Key: "limits.retruns", New: 1}); !errors.As(err, &uk) ||
		uk.Closest != "limits.group_retries" {
		t.Fatalf("unknown key: %v", err)
	}
	var bv BadValueError
	for _, c := range []Change{
		{Key: "output.profile", New: "loud"},
		{Key: "output.turn_end_summary", New: "maybe"},
		{Key: "limits.group_retries", New: "x"},
		{Key: "providers.keys.anthropic", New: "sk-secret-value"},
		{Key: "limits.usd_ceiling_per_run", New: "lots"},
	} {
		if _, err := m.SetConfigItem(c); !errors.As(err, &bv) {
			t.Fatalf("%s=%v: want BadValueError, got %v", c.Key, c.New, err)
		}
	}
	// Text values from the command line parse to the key's kind.
	b := mustSet(t, m, "limits.group_retries", "4", "output.turn_end_summary", "false", "warn.usd_per_run", "2.5")
	if len(b.Changes) != 3 {
		t.Fatalf("want 3 changes, got %d", len(b.Changes))
	}
	it, _ := m.GetConfigItem("limits.pool_min")
	if it.Limit != 6 {
		t.Fatalf("pool_min limit should be the current pool_max, got %v", it.Limit)
	}
	var le LimitError
	if _, err := m.SetConfigItem(Change{Key: "limits.pool_min", New: 7}); !errors.As(err, &le) {
		t.Fatalf("pool_min over pool_max: %v", err)
	}
	mustSet(t, m, "limits.pool_max", 8, "limits.pool_min", 7)
}

func TestAllOrNone(t *testing.T) {
	m, home := newTestManager(t)
	_, err := m.SetConfigItem(
		Change{Key: "limits.group_retries", New: 4},
		Change{Key: "cache.max_mb", New: 9999},
		Change{Key: "output.profile", New: "standard"},
	)
	if err == nil {
		t.Fatal("want an error")
	}
	if readFile(t, home, "settings.json") != "" || readFile(t, home, "state/config.log") != "" ||
		readFile(t, home, "state/config.sha") != "" {
		t.Fatal("a refused call wrote something")
	}
	b := mustSet(t, m, "limits.group_retries", 4, "cache.max_mb", 512, "output.profile", "standard")
	if len(b.Changes) != 3 || b.ID == "" {
		t.Fatalf("batch %+v", b)
	}
	rows := logRows(t, home)
	if len(rows) != 3 {
		t.Fatalf("want 3 audit rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r["batch"] != b.ID {
			t.Fatalf("row %v is not in batch %s", r, b.ID)
		}
	}
	it, _ := m.GetConfigItem("limits.group_retries")
	if it.Value != 4 || it.Source != LayerMachine || it.Default != 3 || it.Limit != 5 {
		t.Fatalf("item %+v", it)
	}
}

func TestIdempotence(t *testing.T) {
	m, home := newTestManager(t)
	mustSet(t, m, "limits.group_retries", 4)
	before := readFile(t, home, "state/config.log")
	b := mustSet(t, m, "limits.group_retries", 4)
	if len(b.Changes) != 0 {
		t.Fatalf("want 0 changes, got %d", len(b.Changes))
	}
	if readFile(t, home, "state/config.log") != before {
		t.Fatal("a repeated call appended a row")
	}
	// A value equal to the built-in one when nothing is set is also a no-op.
	m2, home2 := newTestManager(t)
	if b := mustSet(t, m2, "limits.group_retries", 3); len(b.Changes) != 0 {
		t.Fatal("default value counted as a change")
	}
	if readFile(t, home2, "settings.json") != "" {
		t.Fatal("a no-op created the file")
	}
}

func TestLayerOrderForWallTime(t *testing.T) {
	m, _ := newTestManager(t)
	wall := func() int {
		a, ok := m.GetAgent("builder")
		if !ok {
			t.Fatal("no builder")
		}
		return a.Limits.WallMin
	}
	if wall() != 60 {
		t.Fatalf("role file value: %d", wall())
	}
	mustSet(t, m, "limits.session_wall_min", 45)
	if wall() != 45 {
		t.Fatalf("flat key should beat the role file: %d", wall())
	}
	mustSet(t, m, "agents.builder.session_wall_min", 90)
	if wall() != 90 {
		t.Fatalf("override should beat the flat key: %d", wall())
	}
	if a, _ := m.GetAgent("qa"); a.Limits.WallMin != 45 {
		t.Fatalf("other roles keep the flat key: %d", a.Limits.WallMin)
	}
	it, _ := m.GetConfigItem("agents.builder.session_wall_min")
	if it.Value != 90 || it.Default != 45 || it.Limit != 120 || it.Source != LayerMachine {
		t.Fatalf("item %+v", it)
	}
	var le LimitError
	if _, err := m.SetConfigItem(Change{Key: "agents.builder.session_wall_min", New: 121}); !errors.As(err, &le) ||
		le.Limit != 120 {
		t.Fatalf("over the hard limit: %v", err)
	}
	_, err := m.SetConfigItem(Change{Key: "agents.builder.session_idle_min", New: 31})
	if !errors.As(err, &le) || le.Limit != 30 {
		t.Fatalf("idle over 30: %v", err)
	}
	if _, ok := m.GetAgent("janitor"); ok {
		t.Fatal("a fifth role exists")
	}
	var uk UnknownKeyError
	if _, err := m.SetConfigItem(Change{Key: "agents.janitor.session_idle_min", New: 1}); !errors.As(err, &uk) {
		t.Fatalf("unknown role: %v", err)
	}
	if _, err := m.GetConfigItem("agents.builder.weight"); !errors.As(err, &uk) {
		t.Fatalf("a role field outside the four keys: %v", err)
	}
}

func TestFallbackServesBuiltIn(t *testing.T) {
	m, home := newTestManager(t)
	writeFile(t, home, "settings.json", `{"nope": 1}`)
	it, err := m.GetConfigItem("limits.group_retries")
	if err != nil {
		t.Fatal(err)
	}
	if it.Value != 3 || it.Source != LayerBuiltIn || !it.Fallback {
		t.Fatalf("item %+v", it)
	}
	var uk UnknownKeyError
	if _, err := m.GetConfigItem("nope"); !errors.As(err, &uk) {
		t.Fatalf("unknown key on get: %v", err)
	}
	if a, ok := m.GetAgent("builder"); !ok || a.Limits.WallMin != 60 {
		t.Fatal("agent not served in fallback")
	}
	if err := m.Verify(); err == nil {
		t.Fatal("Verify should report the unreadable file")
	}
}

func TestSnapshotHashIsStable(t *testing.T) {
	m, home := newTestManager(t)
	s1, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := New(home, Known{}, probeAll)
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := m2.Snapshot()
	if s1.Hash != s2.Hash || len(s1.Hash) != 64 {
		t.Fatalf("hashes %q %q", s1.Hash, s2.Hash)
	}
	mustSet(t, m, "limits.group_retries", 4)
	s3, _ := m.Snapshot()
	if s3.Hash == s1.Hash {
		t.Fatal("a changed value kept the hash")
	}
	if s3.Flat["limits.group_retries"].Value != 4 || s3.Flat["limits.group_retries"].Source != LayerMachine {
		t.Fatalf("snapshot %+v", s3.Flat["limits.group_retries"])
	}
	// The model is read live, so it is not part of the pinned hash.
	mustSet(t, m, "agents.reviewer.model", "sonnet")
	s4, _ := m.Snapshot()
	if s4.Hash != s3.Hash {
		t.Fatal("a model change moved the hash")
	}
	mustSet(t, m, "agents.reviewer.session_idle_min", 20)
	s5, _ := m.Snapshot()
	if s5.Hash == s4.Hash || s5.Agents["reviewer"].Limits.IdleMin != 20 {
		t.Fatal("idle override missing from the snapshot")
	}
}

func TestChainsMayNameTheOpenAILinks(t *testing.T) {
	for _, link := range []string{"openai-subscription", "openai-api"} {
		fsys := embedCopy(t)
		p := "agents/builder/agent.json"
		fsys[p].Data = []byte(strings.Replace(string(fsys[p].Data), `"anthropic-api"`, `"`+link+`"`, 1))
		if _, err := newManager(t.TempDir(), fsys, Known{}, probeAll); err != nil {
			t.Errorf("%s: want the chain accepted, got %v", link, err)
		}
	}
}

func TestRoleFilesAreChecked(t *testing.T) {
	edit := func(t *testing.T, role string, f func(string) string) error {
		fsys := embedCopy(t)
		p := "agents/" + role + "/agent.json"
		fsys[p].Data = []byte(f(string(fsys[p].Data)))
		_, err := newManager(t.TempDir(), fsys, Known{}, probeAll)
		return err
	}
	if err := edit(t, "builder", func(s string) string { return s }); err != nil {
		t.Fatalf("unedited copy: %v", err)
	}
	cases := map[string]func(string) string{
		"unknown field":   func(s string) string { return strings.Replace(s, `"v": 1,`, `"v": 1, "bogus": true,`, 1) },
		"bad version":     func(s string) string { return strings.Replace(s, `"v": 1,`, `"v": 2,`, 1) },
		"bad weight":      func(s string) string { return strings.Replace(s, `"standard"`, `"huge"`, 1) },
		"bad chain":       func(s string) string { return strings.Replace(s, `"anthropic-api"`, `"mystery-link"`, 1) },
		"duplicate chain": func(s string) string { return strings.Replace(s, `"anthropic-api"`, `"claude-cli"`, 1) },
		"foreign result": func(s string) string {
			return strings.Replace(s, `komodo:agent/builder/result/1`, `komodo:agent/qa/result/1`, 1)
		},
	}
	for name, f := range cases {
		if err := edit(t, "builder", f); err == nil {
			t.Errorf("%s: want a load error", name)
		}
	}
	// Two roles sharing one result id.
	fsys := embedCopy(t)
	p := "agents/qa/agent.json"
	fsys[p].Data = []byte(
		strings.Replace(string(fsys[p].Data), `komodo:agent/qa/result/1`, `komodo:agent/builder/result/1`, 1),
	)
	if _, err := newManager(t.TempDir(), fsys, Known{}, probeAll); err == nil {
		t.Error("shared result id: want a load error")
	}
	// Known tables: a guard row or tool outside the table fails; an empty table skips the check.
	known := Known{
		GuardRows: map[string]bool{"R1": true},
		Tools: map[string]bool{
			"read":           true,
			"search":         true,
			"edit":           true,
			"write":          true,
			"run":            true,
			"skill":          true,
			"task_done":      true,
			"diff":           true,
			"git_read":       true,
			"submit_finding": true,
			"lint_backlog":   true,
			"run_check":      true,
		},
	}
	if _, err := New(t.TempDir(), known, probeAll); err == nil || !strings.Contains(err.Error(), "B1") {
		t.Errorf("guard row outside known: %v", err)
	}
	known.GuardRows["B1"] = true
	if _, err := New(t.TempDir(), known, probeAll); err != nil {
		t.Errorf("complete known tables: %v", err)
	}
	delete(known.Tools, "task_done")
	if _, err := New(t.TempDir(), known, probeAll); err == nil || !strings.Contains(err.Error(), "task_done") {
		t.Errorf("tool outside known: %v", err)
	}
}

func TestEmbeddedDataIsValid(t *testing.T) {
	m, _ := newTestManager(t)
	if got := len(m.roles); got != 4 {
		t.Fatalf("want 4 roles, got %d", got)
	}
	for _, r := range roleNames {
		a, ok := m.GetAgent(r)
		if !ok || a.V != 1 || len(a.Chain) != 2 || a.Chain[0] != "claude-cli" {
			t.Errorf("role %s: %+v", r, a)
		}
	}
	for _, s := range m.specs {
		if s.Default == nil {
			continue
		}
		v, err := checkValue(s, s.Default, false)
		if err != nil {
			t.Errorf("default of %s fails its own checks: %v", s.Name, err)
		}
		if !equalValues(v, s.Default) {
			t.Errorf("default of %s is not normalised: %v", s.Name, s.Default)
		}
	}
	for _, r := range []string{"planner", "builder", "qa", "reviewer"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "embed", "agents", r, "prompt.md"))
		if isPlaceholder := strings.Contains(string(b), "placeholder"); err != nil || isPlaceholder == (r == "reviewer") {
			t.Errorf("prompt.md of %s: %v (placeholder %v)", r, err, isPlaceholder)
		}
	}
}

func TestResetPlans(t *testing.T) {
	m, home := newTestManager(t)
	mustSet(
		t,
		m,
		"limits.group_retries",
		4,
		"agents.builder.session_wall_min",
		30,
		"agents.builder.session_idle_min",
		20,
		"agents.qa.session_wall_min",
		30,
	)
	cs, err := m.PlanReset("limits.group_retries")
	if err != nil || len(cs) != 1 || cs[0].New != 3 || cs[0].Old != 4 {
		t.Fatalf("key reset: %+v %v", cs, err)
	}
	if cs, _ := m.PlanReset("builder"); len(cs) != 2 {
		t.Fatalf("role reset: %+v", cs)
	}
	cs, _ = m.PlanReset("agents.json")
	if len(cs) != 3 {
		t.Fatalf("file reset: %+v", cs)
	}
	if _, err := m.SetConfigItem(cs...); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, home, "agents.json"), "session_wall_min") {
		t.Fatalf("reset left overrides: %s", readFile(t, home, "agents.json"))
	}
	var uk UnknownKeyError
	if _, err := m.PlanReset("limits.retruns"); !errors.As(err, &uk) {
		t.Fatalf("unknown target: %v", err)
	}
	if err := m.Verify(); err != nil {
		t.Fatalf("Verify after reset: %v", err)
	}
}

func TestHomeFollowsKomodoHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KOMODO_HOME", home)
	got, err := DefaultHome()
	if err != nil || got != home {
		t.Fatalf("DefaultHome: %q %v", got, err)
	}
	m, err := New("", Known{}, probeAll)
	if err != nil {
		t.Fatal(err)
	}
	mustSet(t, m, "limits.group_retries", 4)
	if readFile(t, home, "settings.json") == "" {
		t.Fatal("an empty home did not resolve to KOMODO_HOME")
	}
}
