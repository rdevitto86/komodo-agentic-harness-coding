package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLeavesANarrowerGuardGroupAloneAndAddsItsOwn(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	narrow := `{"matcher":"Bash","hooks":[{"type":"command","command":"my-linter"},{"type":"command","command":"komodo ` +
		`internal guard"}]}`
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"), `{"hooks":{"PreToolUse":[`+narrow+`]}}`)
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	var hooks struct {
		PreToolUse []json.RawMessage `json:"PreToolUse"`
	}
	if err := json.Unmarshal(settingsOf(t, repo)["hooks"], &hooks); err != nil || len(hooks.PreToolUse) != 2 {
		t.Fatalf("groups = %d, %v", len(hooks.PreToolUse), err)
	}
	if compact(hooks.PreToolUse[0]) != narrow {
		t.Errorf("the person's group changed: %s", hooks.PreToolUse[0])
	}
	if own := compact(hooks.PreToolUse[1]); !strings.Contains(own, `"matcher":"Bash|Edit|Write"`) ||
		!strings.Contains(own, `"onFailure":"block"`) {
		t.Errorf("Komodo's own group = %s", own)
	}
	if code, e := h.install(repo, "--repo-only", "--remove"); code != 0 {
		t.Fatalf("remove: exit %d, %+v", code, e.Error)
	}
	if err := json.Unmarshal(settingsOf(t, repo)["hooks"], &hooks); err != nil || len(hooks.PreToolUse) != 1 ||
		compact(hooks.PreToolUse[0]) != narrow {
		t.Fatalf("after remove: %s, %v", settingsOf(t, repo)["hooks"], err)
	}
}

func TestMatcherCovers(t *testing.T) {
	for m, want := range map[string]bool{
		"":                     true,
		"*":                    true,
		"Bash|Edit|Write":      true,
		"Write|Read|Edit|Bash": true,
		"Bash":                 false,
		"Bash|Edit":            false,
		"Bash|Edit|Write.*":    false,
	} {
		if got := matcherCovers(m); got != want {
			t.Errorf("matcherCovers(%q) = %v, want %v", m, got, want)
		}
	}
}
