package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// install runs komodo install in repo with a terminal attached.
func (h *harness) install(repo string, args ...string) (int, Envelope) {
	h.t.Helper()
	h.terminal = true
	return h.envelope(append([]string{"-C", repo, "install"}, args...)...)
}

// readFile returns a file's text, or "" when it is absent.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// countAction counts the rows of an install reply with the action.
func countAction(d InstallData, action string) int {
	n := 0
	for _, c := range d.Changes {
		if c.Action == action {
			n++
		}
	}
	return n
}

func TestInstallDryRunListsFourWritesAndWritesNothing(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	code, e := h.install(repo, "--repo-only", "--hosts", "claude", "--dry-run")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if d := dataOf[InstallData](t, e); countAction(d, "write") != 4 {
		t.Fatalf("rows = %+v", d.Changes)
	}
	for _, f := range []string{"AGENTS.md", "CLAUDE.md", ".claude/settings.json", ".git/komodo/install.json"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err == nil {
			t.Errorf("dry run wrote %s", f)
		}
	}
}

func TestInstallWritesOnlyTheOwnedParts(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	mustWrite(t, filepath.Join(repo, "AGENTS.md"), "# Mine\n\nKeep this text.\n")
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"), `{"model":"opus","permissions":{"allow":["Bash(ls)"]}}`)
	code, e := h.install(repo, "--repo-only", "--hosts", "claude")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	agents := readFile(t, filepath.Join(repo, "AGENTS.md"))
	if !strings.HasPrefix(agents, "# Mine\n\nKeep this text.\n") ||
		!strings.Contains(agents, "<!-- komodo:begin min=1.0.0-beta.6 -->") ||
		!strings.Contains(agents, "<!-- komodo:end -->") {
		t.Fatalf("AGENTS.md = %q", agents)
	}
	if got := readFile(t, filepath.Join(repo, "CLAUDE.md")); got != "@AGENTS.md\n" {
		t.Fatalf("CLAUDE.md = %q", got)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repo, ".claude", "settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings["hooks"]; ok {
		t.Errorf("install wrote a hooks entry: %s", settings["hooks"])
	}
	text := string(settings["permissions"])
	for _, want := range []string{
		"Bash(ls)",
		"Bash(komodo *)",
		"Bash(komodo config set*)",
		"Bash(komodo install*)",
		"Bash(komodo update*)",
	} {
		if !strings.Contains(
			strings.ReplaceAll(strings.ReplaceAll(text, " ", ""), "\n", ""),
			strings.ReplaceAll(want, " ", ""),
		) {
			t.Errorf("settings lack %q:\n%s", want, text)
		}
	}
	if string(settings["model"]) != `"opus"` {
		t.Errorf("model = %s", settings["model"])
	}
	var m Manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repo, ".git", "komodo", "install.json"))), &m); err != nil ||
		len(m.Entries) != 6 {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
}

func TestInstallRerunMakesNoChanges(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	if code, e := h.install(repo, "--hosts", "claude"); code != 0 {
		t.Fatalf("first: exit %d, %+v", code, e.Error)
	}
	before := readFile(t, filepath.Join(repo, "AGENTS.md")) + readFile(t, filepath.Join(repo, ".claude", "settings.json"))
	code, e := h.install(repo, "--hosts", "claude")
	d := dataOf[InstallData](t, e)
	if code != 0 || len(d.Changes) != 0 || !strings.Contains(e.Report, "0 changes") {
		t.Fatalf("second: exit %d, rows %+v, report %q", code, d.Changes, e.Report)
	}
	if after := readFile(
		t,
		filepath.Join(repo, "AGENTS.md"),
	) + readFile(t, filepath.Join(repo, ".claude", "settings.json")); after != before {
		t.Fatal("a rerun rewrote a file")
	}
}

func TestInstallKeepsAHandEditedBlockUnlessForced(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	h.install(repo, "--repo-only", "--hosts", "claude")
	path := filepath.Join(repo, "AGENTS.md")
	edited := strings.Replace(readFile(t, path), "#### Komodo harness", "#### My own heading", 1)
	mustWrite(t, path, edited)
	code, e := h.install(repo, "--repo-only", "--hosts", "claude")
	d := dataOf[InstallData](t, e)
	if code != 0 || len(d.Edited) != 1 || d.Edited[0] != "AGENTS.md" || readFile(t, path) != edited {
		t.Fatalf("exit %d, edited %v", code, d.Edited)
	}
	if code, _ = h.install(repo, "--repo-only", "--hosts", "claude", "--force"); code != 0 ||
		!strings.Contains(readFile(t, path), "#### Komodo harness") {
		t.Fatalf("force: exit %d, AGENTS.md = %q", code, readFile(t, path))
	}
}

func TestInstallRemoveLeavesNoEntriesAndKeepsOtherText(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	mustWrite(t, filepath.Join(repo, "AGENTS.md"), "# Mine\n")
	mustWrite(t, filepath.Join(repo, ".claude", "settings.json"), `{"model":"opus"}`)
	h.install(repo, "--repo-only", "--hosts", "claude")
	code, e := h.install(repo, "--repo-only", "--remove")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if got := readFile(t, filepath.Join(repo, "AGENTS.md")); strings.Contains(got, "komodo:") ||
		!strings.Contains(got, "# Mine") {
		t.Fatalf("AGENTS.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md was left")
	}
	if got := strings.Join(
		strings.Fields(readFile(t, filepath.Join(repo, ".claude", "settings.json"))),
		"",
	); got != `{"model":"opus"}` {
		t.Errorf("settings = %q", got)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repo, ".git", "komodo", "install.json"))), &m); err != nil ||
		len(m.Entries) != 0 {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
}

func TestInstallRemoveKeepsAHandEditedOwnedFile(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	h.install(repo, "--repo-only", "--hosts", "claude")
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n@docs/extra.md\n")
	// CLAUDE.md still holds the owned line, so only a changed owned line counts as a hand edit.
	mustWrite(
		t,
		filepath.Join(repo, "AGENTS.md"),
		strings.Replace(readFile(t, filepath.Join(repo, "AGENTS.md")), "#### Komodo harness", "#### Edited", 1),
	)
	code, e := h.install(repo, "--repo-only", "--remove")
	d := dataOf[InstallData](t, e)
	if code != 0 || len(d.Edited) != 1 || !strings.Contains(readFile(t, filepath.Join(repo, "AGENTS.md")), "#### Edited") {
		t.Fatalf("exit %d, edited %v", code, d.Edited)
	}
	if got := readFile(t, filepath.Join(repo, "CLAUDE.md")); got != "@docs/extra.md\n" {
		t.Errorf("CLAUDE.md = %q", got)
	}
}

func TestInstallBrokenMarkersStopTheStep(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "")
	broken := "# Mine\n<!-- komodo:begin min=1.0.0 -->\nno end marker\n"
	mustWrite(t, filepath.Join(repo, "AGENTS.md"), broken)
	code, e := h.install(repo, "--repo-only")
	if code != 1 || e.Error == nil || !strings.Contains(e.Error.Reason, "AGENTS.md") ||
		readFile(t, filepath.Join(repo, "AGENTS.md")) != broken {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
}

func TestInstallPinOnlyRises(t *testing.T) {
	h := newHarness(t)
	repo := repoDir(t, "2.0.0")
	if code, e := h.install(repo, "--repo-only"); code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if got := readFile(t, filepath.Join(repo, "AGENTS.md")); !strings.Contains(got, "komodo:begin min=2.0.0 -->") {
		t.Fatalf("AGENTS.md = %q", got)
	}
}

func TestInstallMachineStepsPlaceBinaryPathAndRecords(t *testing.T) {
	h := newHarness(t)
	repo := t.TempDir()
	code, e := h.install(repo)
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if got := readFile(t, filepath.Join(h.home, "bin", "komodo")); got != "komodo test binary" {
		t.Fatalf("bin/komodo = %q", got)
	}
	if got := readFile(t, filepath.Join(h.home, "versions", "1.0.0-beta.6+dev", "komodo")); got != "komodo test binary" {
		t.Fatalf("versions copy = %q", got)
	}
	if got := readFile(t, filepath.Join(h.userHome, ".zshrc")); !strings.Contains(got, filepath.Join(h.home, "bin")) {
		t.Fatalf(".zshrc = %q", got)
	}
	m, err := state.NewMachine(h.home, "cli")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := state.LoadMachine[state.InstallBody](m, state.KindInstall)
	if err != nil || len(rec.Body.Entries) < 3 {
		t.Fatalf("install.json = %+v, %v", rec, err)
	}
	prov, err := state.LoadMachine[state.ProvidersBody](m, state.KindProviders)
	if err != nil || len(prov.Body.Links[string(llm.ClaudeCLI)].Probe) == 0 {
		t.Fatalf("providers.json = %+v, %v", prov, err)
	}
	d := dataOf[InstallData](t, e)
	if len(d.Skipped) == 0 {
		t.Errorf("repository steps outside a repository were not reported as skipped: %+v", d)
	}
	code, e = h.install(repo)
	if d = dataOf[InstallData](t, e); code != 0 || len(d.Changes) != 0 {
		t.Fatalf("rerun: exit %d, rows %+v", code, d.Changes)
	}
	if got := strings.Count(readFile(t, filepath.Join(h.userHome, ".zshrc")), filepath.Join(h.home, "bin")); got != 1 {
		t.Fatalf("rerun wrote the path line %d times", got)
	}
}

func TestInstallPathStepUsesALocalBinSymlinkWhenOnPath(t *testing.T) {
	h := newHarness(t)
	local := filepath.Join(h.userHome, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", local+":/usr/bin:/bin")
	if code, e := h.install(t.TempDir(), "--machine-only"); code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	dest, err := os.Readlink(filepath.Join(local, "komodo"))
	if err != nil || dest != filepath.Join(h.home, "bin", "komodo") {
		t.Fatalf("symlink = %q, %v", dest, err)
	}
	if _, err := os.Stat(filepath.Join(h.userHome, ".zshrc")); err == nil {
		t.Error("a shell file was written beside the symlink")
	}
}

func TestInstallNoPathAndFromAndRepoOnlyOutsideARepository(t *testing.T) {
	h := newHarness(t)
	from := filepath.Join(t.TempDir(), "komodo-new")
	mustWrite(t, from, "#!/bin/sh\necho 1.2.3\n")
	if err := os.Chmod(from, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, e := h.install(t.TempDir(), "--machine-only", "--no-path", "--from", from); code != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if got := readFile(t, filepath.Join(h.home, "versions", "1.2.3", "komodo")); !strings.Contains(got, "echo 1.2.3") {
		t.Fatalf("--from copy = %q", got)
	}
	if _, err := os.Stat(filepath.Join(h.userHome, ".zshrc")); err == nil {
		t.Error("--no-path wrote a shell file")
	}
	code, e := h.install(t.TempDir(), "--repo-only")
	if code != 1 || e.Error.Code != "no_repo" {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, err := core.ParseSemVer("1.2.3"); err != nil {
		t.Fatal(err)
	}
}

// mustWrite writes a file and its folder for a test.
func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
