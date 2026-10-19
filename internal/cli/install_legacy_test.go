package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyHookJSON = `{"type":"command","command":"komodo internal guard","onFailure":"block"}`

// seedLegacyHook writes the hook entry an earlier install owned, with its manifest entry, into a repository.
func seedLegacyHook(t *testing.T, repo, settings, hook string) {
	t.Helper()
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"), settings)
	path := filepath.Join(repo, ".git", "komodo", "install.json")
	var m Manifest
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
	}
	m.Entries = append(m.Entries, Entry{
		Target: filepath.Join(".claude", "settings.json"),
		Entry:  legacyHookEntry,
		Hash:   sha([]byte(hook)),
		Step:   stepSettings,
	})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, string(b))
}

func TestInstallWritesNoHook(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	if strings.Contains(readFile(t, filepath.Join(repo, ".claude", "settings.json")), "hooks") {
		t.Errorf("settings hold a hook: %s", readFile(t, filepath.Join(repo, ".claude", "settings.json")))
	}
}

func TestRerunRemovesOnlyTheHookAnEarlierInstallWrote(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	foreign := `{"matcher":"Read|Bash","hooks":[{"type":"command","command":"my-linter"},` + legacyHookJSON + `]}`
	alone := `{"matcher":"Bash|Edit|Write","hooks":[` + legacyHookJSON + `]}`
	seedLegacyHook(t, repo, `{"hooks":{"PreToolUse":[`+foreign+`,`+alone+`]},"model":"opus"}`, legacyHookJSON)
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	flat := strings.Join(strings.Fields(readFile(t, filepath.Join(repo, ".claude", "settings.json"))), "")
	if !strings.Contains(flat, `"command":"my-linter"`) || !strings.Contains(flat, `"matcher":"Read|Bash"`) ||
		strings.Contains(flat, "internalguard") || strings.Contains(flat, `"matcher":"Bash|Edit|Write"`) ||
		!strings.Contains(flat, `"model":"opus"`) {
		t.Errorf("after rerun: %s", flat)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repo, ".git", "komodo", "install.json"))), &m); err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Entries {
		if e.Entry == legacyHookEntry {
			t.Errorf("the manifest still owns the hook: %+v", e)
		}
	}
}

func TestRemoveDropsTheHookAnEarlierInstallWrote(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	settings := `{"hooks":{"PreToolUse":[{"matcher":"Bash|Edit|Write","hooks":[` + legacyHookJSON + `]}]}}`
	seedLegacyHook(t, repo, settings, legacyHookJSON)
	if code, e := h.install(repo, "--repo-only", "--remove"); code != 0 {
		t.Fatalf("remove: exit %d, %+v", code, e.Error)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("settings left behind: %s", readFile(t, filepath.Join(repo, ".claude", "settings.json")))
	}
}

func TestRerunKeepsAHandEditedLegacyHook(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	edited := `{"type":"command","command":"komodo internal guard","onFailure":"allow"}`
	seedLegacyHook(t, repo, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+edited+`]}]}}`, legacyHookJSON)
	code, e := h.install(repo, "--repo-only", "--hosts", "claude")
	if code != 0 || !strings.Contains(readFile(t, filepath.Join(repo, ".claude", "settings.json")), `"allow"`) {
		t.Fatalf("exit %d, %+v, settings %s", code, e.Error, readFile(t, filepath.Join(repo, ".claude", "settings.json")))
	}
}
