package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// settingsOf reads .claude/settings.json of a repository as raw top-level values.
func settingsOf(t *testing.T, repo string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repo, ".claude", "settings.json"))), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallNeverOwnsWhatWasAlreadyThere(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	mustWrite(
		t,
		filepath.Join(repo, ".claude", "settings.json"),
		`{"permissions":{"allow":["Bash(komodo *)","Bash(npm test && lint)"]}}`,
	)
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repo, ".git", "komodo", "install.json"))), &m); err != nil ||
		len(m.Entries) != 5 {
		t.Fatalf(
			"manifest holds %d entries, want 5 (the pre-existing import and allow rule are not owned): %+v, %v",
			len(m.Entries),
			m,
			err,
		)
	}
	if code, e := h.install(repo, "--repo-only", "--remove"); code != 0 {
		t.Fatalf("remove: exit %d, %+v", code, e.Error)
	}
	if got := readFile(t, filepath.Join(repo, "CLAUDE.md")); got != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q", got)
	}
	if got := string(settingsOf(t, repo)["permissions"]); !strings.Contains(got, "Bash(komodo *)") ||
		!strings.Contains(got, "npm test") {
		t.Errorf("permissions after remove = %s", got)
	}
}

func TestInstallEditsOnlyTheGuardInsideAForeignHookGroup(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Read|Bash|Edit|Write","hooks":[{"type":"command","command":"my-linter"},`+
			`{"type":"command","command":"komodo internal guard"}]}]}}`)
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	flat := strings.Join(strings.Fields(string(settingsOf(t, repo)["hooks"])), "")
	for _, want := range []string{`"matcher":"Read|Bash|Edit|Write"`, `"command":"my-linter"`, `"onFailure":"block"`} {
		if !strings.Contains(flat, want) {
			t.Errorf("after install the hooks lack %s: %s", want, flat)
		}
	}
	if strings.Count(flat, "komodointernalguard") != 1 {
		t.Errorf("the guard is listed %d times: %s", strings.Count(flat, "komodointernalguard"), flat)
	}
	if code, e := h.install(repo, "--repo-only", "--remove"); code != 0 {
		t.Fatalf("remove: exit %d, %+v", code, e.Error)
	}
	flat = strings.Join(strings.Fields(string(settingsOf(t, repo)["hooks"])), "")
	if !strings.Contains(flat, `"command":"my-linter"`) || !strings.Contains(flat, `"matcher":"Read|Bash|Edit|Write"`) ||
		strings.Contains(flat, "komodointernalguard") {
		t.Errorf("after remove: %s", flat)
	}
}

func TestSettingsRewriteKeepsKeyOrderAndDoesNotEscape(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"),
		`{"model":"opus","permissions":{"allow":["Bash(npm test && lint)"],"ask":["Bash(rm *)"]},"alpha":1}`)
	if code, e := h.install(repo, "--repo-only", "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	text := readFile(t, filepath.Join(repo, ".claude", "settings.json"))
	if !strings.Contains(text, "npm test && lint") || strings.Contains(text, "\\u0026") {
		t.Errorf("the person's rule was escaped:\n%s", text)
	}
	if a, b, c := strings.Index(
		text,
		`"model"`,
	), strings.Index(text, `"permissions"`), strings.Index(text, `"alpha"`); a >= b || b >= c {
		t.Errorf("key order changed:\n%s", text)
	}
}

func TestUpdateKeepsOldVersionsWhenNoVersionsInUseSourceIsWired(t *testing.T) {
	h := newHarness(t)
	seedBin(t, h)
	mustWrite(t, filepath.Join(h.home, "versions", "0.9.0", "komodo"), "old")
	r := newRelease(t, "1.0.1", "new binary")
	code, e := h.update(r)
	if code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, err := os.Stat(filepath.Join(h.home, "versions", "0.9.0", "komodo")); err != nil {
		t.Fatalf("a version that unfinished groups may need was deleted: %v", err)
	}
	notes, _ := e.Data.(map[string]any)["notes"].([]any)
	if len(notes) == 0 || !strings.Contains(notes[0].(string), "kept") {
		t.Fatalf("the reply does not say old versions were kept: %v", e.Data)
	}
}

func TestDoctorAndInstallReportLinksNotWiredInsteadOfFailing(t *testing.T) {
	h := newHarness(t)
	h.host.Links = nil
	repo := installed(t, h)
	code, checks := h.doctor(repo)
	if code != 0 || checks["links"].Result != "skip" || checks["links"].Fix != "" ||
		!strings.Contains(checks["links"].Detail, "not wired") {
		t.Fatalf("exit %d, checks = %+v", code, checks)
	}
	_, e := h.install(t.TempDir(), "--machine-only", "--no-path")
	if d := dataOf[InstallData](t, e); !strings.Contains(strings.Join(d.Skipped, ";"), "probe") {
		t.Fatalf("skipped = %v", d.Skipped)
	}
}

func TestClaimLockTakeoverIsExclusiveAndReleaseChecksOwnership(t *testing.T) {
	home := t.TempDir()
	lock := filepath.Join(home, "state", "update.lock")
	mustWrite(t, lock, "crashed")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := claimLock(home, time.Now); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims took over one stale lock", wins.Load())
	}
	home2 := t.TempDir()
	release, err := claimLock(home2, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home2, "state", "update.lock")
	mustWrite(t, other, "another update")
	release()
	if readFile(t, other) != "another update" {
		t.Fatal("release deleted a lock it no longer owned")
	}
	if lockStaleAfter <= downloadTimeout {
		t.Fatalf("a lock goes stale in %s, no longer than a download may take (%s)", lockStaleAfter, downloadTimeout)
	}
}

func TestInstallTakesTheUpdateLockForTheBinary(t *testing.T) {
	h := newHarness(t)
	mustWrite(t, filepath.Join(h.home, "state", "update.lock"), "an update")
	code, e := h.install(t.TempDir(), "--machine-only", "--no-path")
	if code != 1 || e.Error == nil || !strings.Contains(e.Error.Reason, "update") {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, err := os.Stat(filepath.Join(h.home, "bin", "komodo")); err == nil {
		t.Fatal("the binary was written while an update held the lock")
	}
	if code, e = h.install(t.TempDir(), "--machine-only", "--no-path"); code != 1 {
		t.Fatalf("second try: exit %d, %+v", code, e.Error)
	}
	if err := os.Remove(filepath.Join(h.home, "state", "update.lock")); err != nil {
		t.Fatal(err)
	}
	if code, e = h.install(t.TempDir(), "--machine-only", "--no-path"); code != 0 {
		t.Fatalf("after release: exit %d, %+v", code, e.Error)
	}
	if _, err := os.Stat(filepath.Join(h.home, "state", "update.lock")); err == nil {
		t.Error("install left update.lock behind")
	}
}

func TestMergeInstallBodyKeepsRecordsAddedMeanwhile(t *testing.T) {
	hash := strings.Repeat("a", 64)
	body := state.InstallBody{Entries: []state.InstallEntry{
		{Target: "/h/bin/komodo", Hash: strings.Repeat("b", 64), Step: 1},
		{Target: "/elsewhere", Hash: hash, Step: 2},
	}}
	keys := map[string]bool{"/h/bin/komodo\x00": true, "/h/old\x00": true}
	after := []Entry{{Target: "/h/bin/komodo", Hash: hash, Step: 1}}
	if !mergeInstallBody(&body, keys, after, Selections{Hosts: []string{"claude"}}) {
		t.Fatal("a changed body reported no change")
	}
	if len(body.Entries) != 2 || body.Entries[0].Hash != hash || body.Entries[1].Target != "/elsewhere" ||
		len(body.Selections.Hosts) != 1 {
		t.Fatalf("body = %+v", body)
	}
}

// recordingTB records the failure message of a guard check.
type recordingTB struct{ msg string }

// Helper marks nothing.
func (*recordingTB) Helper() {}

// Fatalf records the message.
func (r *recordingTB) Fatalf(f string, a ...any) { r.msg = f }

func TestHarnessGuardFindsARepositoryAboveTheTempFolder(t *testing.T) {
	outer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "tmp")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	var tb recordingTB
	mustBeOutsideRepo(&tb, inner)
	if tb.msg == "" {
		t.Fatal("a temporary folder inside a repository was accepted")
	}
	tb = recordingTB{}
	mustBeOutsideRepo(&tb, t.TempDir())
	if tb.msg != "" {
		t.Fatalf("a clean temporary folder was refused: %s", tb.msg)
	}
}
