package task

import (
	"errors"
	"strings"
	"testing"
)

func TestAGuardThatFailsToLoadBlocksTheGroupUnderSetupAndNamesTheRulesFile(t *testing.T) {
	old := loadPolicy
	loadPolicy = func() (*policy, error) { return nil, errors.New("read policy.json: bad row") }
	t.Cleanup(func() { loadPolicy = old })

	_, err := loadGuard(t.TempDir(), Binding{Agent: "builder", Worktree: t.TempDir()}, 3)

	var be *blockErr
	if !errors.As(err, &be) || be.kind != EscBlocked || be.reason != "setup" {
		t.Fatalf("error %v, want a setup block", err)
	}

	d, _ := be.detail.(map[string]string)
	if !strings.Contains(d["file"], "policy.json") || !strings.Contains(d["error"], "bad row") {
		t.Fatalf("detail %v does not name the rules file and the cause", d)
	}
}
