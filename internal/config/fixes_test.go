package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStaleLockRaceHasOneWinner(t *testing.T) {
	m, home := newTestManager(t)
	lock := filepath.Join(home, "state", "settings.lock")
	old := lockWait
	lockWait = 120 * time.Millisecond
	defer func() { lockWait = old }()
	for i := 0; i < 40; i++ {
		if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lock, []byte("999-dead"), 0o644); err != nil {
			t.Fatal(err)
		}
		stale := time.Now().Add(-time.Minute)
		if err := os.Chtimes(lock, stale, stale); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		done := make(chan struct{})
		results := make(chan error, 2)
		unlocks := make(chan func(), 2)
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				unlock, err := m.lock()
				results <- err
				if err == nil {
					<-done
					unlocks <- unlock
				}
			}()
		}
		wins := 0
		for j := 0; j < 2; j++ {
			if err := <-results; err == nil {
				wins++
			} else if !errors.Is(err, ErrLocked) {
				t.Fatalf("round %d: %v", i, err)
			}
		}
		close(done)
		wg.Wait()
		close(unlocks)
		for u := range unlocks {
			u()
		}
		if wins != 1 {
			t.Fatalf("round %d: %d writers hold the lock, want 1", i, wins)
		}
	}
}

func TestUnlockKeepsAnotherWritersLock(t *testing.T) {
	m, home := newTestManager(t)
	unlock, err := m.lock()
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(home, "state", "settings.lock")
	if err := os.WriteFile(lock, []byte("1-someone-else"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("unlock removed a lock it no longer held: %v", err)
	}
}

func TestResetRepairsAnUnreadableFile(t *testing.T) {
	for _, tc := range []struct{ file, target, body string }{
		{"settings.json", "settings.json", `{"nope": 1}`},
		{"settings.json", "limits.returns", `{"nope": 1}`},
		{"agents.json", "agents.json", `{"builder": {"bogus": 1}}`},
		{"agents.json", "builder", `{"builder": {"bogus": 1}}`},
	} {
		m, home := newTestManager(t)
		mustSet(t, m, "limits.returns", 4, "agents.tester.retries", 2)
		other := map[string]string{"settings.json": "agents.json", "agents.json": "settings.json"}[tc.file]
		keep := readFile(t, home, other)
		writeFile(t, home, tc.file, tc.body)
		if _, err := m.Accept(); err != nil {
			t.Fatal(err)
		}
		cs, err := m.PlanReset(tc.target)
		if err != nil {
			t.Fatalf("%s: PlanReset: %v", tc.target, err)
		}
		if _, err := m.SetConfigItem(cs...); err != nil {
			t.Fatalf("%s: SetConfigItem: %v", tc.target, err)
		}
		if err := m.Verify(); err != nil {
			t.Fatalf("%s: Verify: %v", tc.target, err)
		}
		if _, err := m.Snapshot(); err != nil {
			t.Fatalf("%s: Snapshot after repair: %v", tc.target, err)
		}
		if got := readFile(t, home, other); got != keep {
			t.Errorf("%s: the readable file %s changed:\n%s", tc.target, other, got)
		}
	}
}

func TestSetRepairsAnUnreadableFile(t *testing.T) {
	m, home := newTestManager(t)
	writeFile(t, home, "settings.json", `{"nope": 1}`)
	if _, err := m.Accept(); err != nil {
		t.Fatal(err)
	}
	mustSet(t, m, "limits.returns", 4)
	if got := readFile(t, home, "settings.json"); strings.Contains(got, "nope") || !strings.Contains(got, `"limits.returns": 4`) {
		t.Fatalf("settings.json = %s", got)
	}
	it, _ := m.GetConfigItem("limits.returns")
	if it.Value != 4 || it.Fallback {
		t.Fatalf("item %+v", it)
	}
}

func TestFamilyMemberMustBeKnown(t *testing.T) {
	m, home := newTestManager(t)
	var uk UnknownKeyError
	if _, err := m.SetConfigItem(Change{Key: "limits.link_sessions.typo-link", New: 5}); !errors.As(err, &uk) {
		t.Fatalf("typo member on set: %v", err)
	}
	if _, err := m.GetConfigItem("limits.link_sessions.typo-link"); !errors.As(err, &uk) {
		t.Fatalf("typo member on get: %v", err)
	}
	writeFile(t, home, "settings.json", `{"limits.link_sessions.typo-link": 5}`)
	if err := m.Verify(); err == nil {
		t.Fatal("a file naming a typo member passed strict reading")
	}
	m2, _ := newTestManager(t)
	for link, want := range map[string]int{"claude-cli": 3, "anthropic-api": 3, "local": 1} {
		if it, err := m2.GetConfigItem("limits.link_sessions." + link); err != nil || it.Default != want || it.Value != want {
			t.Errorf("%s: %+v %v", link, it, err)
		}
	}
}

func TestLinkMembersMatchValidLinks(t *testing.T) {
	m, _ := newTestManager(t)
	found := false
	for _, s := range m.specs {
		if s.Name != "limits.link_sessions.<link>" {
			continue
		}
		found = true
		var got []string
		for k := range s.Members {
			got = append(got, k)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(validLinks))
		if !slices.Equal(got, want) {
			t.Fatalf("members %v, valid links %v", got, want)
		}
	}
	if !found {
		t.Fatal("no link family in the registry")
	}
}

func TestProvidersLocalKeys(t *testing.T) {
	m, home := newTestManager(t)
	mustSet(t, m, "providers.local.ollama.url", "http://127.0.0.1:11434", "providers.local.ollama.ctx", 32768, "providers.local.ollama.self_hosted", true)
	it, err := m.GetConfigItem("providers.local.ollama.url")
	if err != nil || it.Value != "http://127.0.0.1:11434" || it.Source != LayerMachine {
		t.Fatalf("%+v %v", it, err)
	}
	if it, err := m.GetConfigItem("providers.local.ollama.first_byte_s"); err != nil || it.Value != nil {
		t.Fatalf("unset member: %+v %v", it, err)
	}
	var uk UnknownKeyError
	if _, err := m.SetConfigItem(Change{Key: "providers.local.ollama.colour", New: "red"}); !errors.As(err, &uk) {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := m.SetConfigItem(Change{Key: "providers.local.url", New: "x"}); !errors.As(err, &uk) {
		t.Fatalf("missing endpoint name: %v", err)
	}
	other, err := New(home, Known{}, probeAll)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if cs, err := m.PlanReset("providers.local.ollama.url"); err != nil || len(cs) != 1 || cs[0].New != nil {
		t.Fatalf("reset: %+v %v", cs, err)
	}
}

func TestReviewModeKey(t *testing.T) {
	m, _ := newTestManager(t)
	if it, err := m.GetConfigItem("review.mode"); err != nil || it.Value != "thorough" {
		t.Fatalf("%+v %v", it, err)
	}
	var uk UnknownKeyError
	if _, err := m.GetConfigItem("mode"); !errors.As(err, &uk) {
		t.Fatalf("bare mode key: %v", err)
	}
}

func TestAgentModelWithoutOverrideNamesTheWeight(t *testing.T) {
	m, _ := newTestManager(t)
	for role, a := range m.roles {
		it, err := m.GetConfigItem("agents." + role + ".model")
		if err != nil || it.Value != nil || it.Source != LayerBuiltIn || it.Default != "weight:"+string(a.Weight) {
			t.Errorf("%s: %+v %v", role, it, err)
		}
	}
	mustSet(t, m, "agents.builder.model", "m-1")
	it, _ := m.GetConfigItem("agents.builder.model")
	if it.Value != "m-1" || it.Source != LayerMachine || it.Default != "weight:"+string(m.roles["builder"].Weight) {
		t.Fatalf("%+v", it)
	}
	cs, err := m.PlanReset("agents.builder.model")
	if err != nil || len(cs) != 1 || cs[0].New != nil {
		t.Fatalf("reset: %+v %v", cs, err)
	}
	if _, err := m.SetConfigItem(cs...); err != nil {
		t.Fatal(err)
	}
	if it, _ := m.GetConfigItem("agents.builder.model"); it.Value != nil {
		t.Fatalf("after reset: %+v", it)
	}
}
