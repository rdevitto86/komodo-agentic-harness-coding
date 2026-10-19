package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// fakeDisk answers the disk state of each entry from a map keyed by target and entry.
func fakeDisk(m map[string]diskState) func(bool, Entry) diskState {
	return func(_ bool, e Entry) diskState { return m[e.Target+"|"+e.Entry] }
}

func TestPlanInstallRerunWritesAndPrunesNothing(t *testing.T) {
	want := []item{
		{Entry: Entry{Target: "AGENTS.md", Hash: "aa", Step: 4}},
		{Entry: Entry{Target: "CLAUDE.md", Hash: "bb", Step: 5}},
	}
	rec := []Entry{{Target: "AGENTS.md", Hash: "aa", Step: 4}, {Target: "CLAUDE.md", Hash: "bb", Step: 5}}
	disk := fakeDisk(map[string]diskState{"AGENTS.md|": {true, "aa"}, "CLAUDE.md|": {true, "bb"}})
	for _, r := range planInstall(want, rec, disk, false) {
		if r.action != "keep" {
			t.Errorf("%s: %s (%s)", r.it.Target, r.action, r.reason)
		}
	}
}

func TestPlanInstallHandEditKeepsUnlessForced(t *testing.T) {
	want := []item{{Entry: Entry{Target: "AGENTS.md", Hash: "aa", Step: 4}}}
	rec := []Entry{{Target: "AGENTS.md", Hash: "aa", Step: 4}}
	disk := fakeDisk(map[string]diskState{"AGENTS.md|": {true, "edited"}})
	if r := planInstall(want, rec, disk, false)[0]; r.action != "keep" || !r.edited {
		t.Fatalf("no force: %+v", r)
	}
	if r := planInstall(want, rec, disk, true)[0]; r.action != "write" || r.edited {
		t.Fatalf("force: %+v", r)
	}
}

func TestPlanInstallPrunesWhatIsNoLongerWanted(t *testing.T) {
	rec := []Entry{{Target: "CLAUDE.md", Hash: "bb", Step: 5}, {Target: "AGENTS.md", Hash: "aa", Step: 4}}
	disk := fakeDisk(map[string]diskState{"CLAUDE.md|": {true, "bb"}, "AGENTS.md|": {true, "x"}})
	rows := planInstall(nil, rec, disk, false)
	if len(rows) != 2 || rows[0].it.Target != "AGENTS.md" || rows[0].action != "keep" || rows[1].action != "prune" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestPlanInstallRewritesWhenTheWantedContentChanged(t *testing.T) {
	want := []item{{Entry: Entry{Target: "AGENTS.md", Hash: "new", Step: 4}}}
	rec := []Entry{{Target: "AGENTS.md", Hash: "old", Step: 4}}
	disk := fakeDisk(map[string]diskState{"AGENTS.md|": {true, "old"}})
	if r := planInstall(want, rec, disk, false)[0]; r.action != "write" || r.reason != "content changed" {
		t.Fatalf("row = %+v", r)
	}
}

func TestNextPin(t *testing.T) {
	for _, c := range []struct{ existing, running, seen, want string }{
		{"", "1.0.0-beta.6+dev", "", "1.0.0-beta.6"},
		{"1.0.0", "1.0.0-beta.6+dev", "", "1.0.0"},
		{"1.0.0-beta.5", "1.0.0-beta.6+dev", "", "1.0.0-beta.6"},
		{"", "1.2.0", "1.1.0", "1.1.0"},
		{"1.3.0", "1.2.0", "1.1.0", "1.3.0"},
	} {
		got, err := nextPin(c.existing, c.running, c.seen)
		if err != nil || got != c.want {
			t.Errorf("nextPin(%q, %q, %q) = %q, %v; want %q", c.existing, c.running, c.seen, got, err, c.want)
		}
	}
	if _, err := nextPin("", "not-a-version", ""); err == nil {
		t.Error("an unparsable running version was accepted")
	}
}

func TestFindBlockNamesBrokenMarkers(t *testing.T) {
	for _, text := range []string{
		"<!-- komodo:begin min=1.0.0 -->\nx\n",
		"x\n<!-- komodo:end -->\n",
		"<!-- komodo:begin min=1.0.0 -->\n<!-- komodo:begin min=1.0.0 -->\n<!-- komodo:end -->\n",
		"<!-- komodo:end -->\n<!-- komodo:begin min=1.0.0 -->\n",
	} {
		if _, _, err := findBlock(text); err == nil || !strings.Contains(err.Error(), "AGENTS.md") {
			t.Errorf("%q: err = %v", text, err)
		}
	}
	b, ok, err := findBlock("a\n<!-- komodo:begin min=1.2.3 -->\nbody\n<!-- komodo:end -->\nz\n")
	if err != nil || !ok || b.Min != "1.2.3" {
		t.Fatalf("block = %+v, %v, %v", b, ok, err)
	}
}

func TestPutAndDropBlockRoundTrip(t *testing.T) {
	block := renderBlock("1.0.0", "/h/bin/komodo", "", "")
	for _, orig := range []string{"", "# Mine\n", "# Mine", "# Mine\n\nMore.\n"} {
		with, err := putBlock(orig, block)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := putBlock(with, block)
		if again != with {
			t.Errorf("%q: a second put changed the text", orig)
		}
		got, err := dropBlock(with)
		want := orig
		if orig != "" && !strings.HasSuffix(orig, "\n") {
			want = orig + "\n"
		}
		if err != nil || got != want {
			t.Errorf("%q: drop gave %q, want %q (%v)", orig, got, want, err)
		}
	}
}

func TestBlockSetsTheStyleApartUnderTheBlockHeading(t *testing.T) {
	style := "# Komodo output style: adhd\n\nRelay the report.\n\n- Line one.\n"
	block := renderBlock("1.0.0", "/h/bin/komodo", "", style)
	if !strings.Contains(block, "- **Report:** relay the envelope's `report` field verbatim.\n\n##### Komodo output style: adhd\n") {
		t.Errorf("the style does not follow a blank line as a heading under the block heading:\n%s", block)
	}
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "# ") {
			t.Errorf("a top-level heading sits inside the block: %q", line)
		}
	}
}

func TestBlockNamesOnlyCommandsTheTableHolds(t *testing.T) {
	block := renderBlock("1.0.0", "/h/bin/komodo", "", "")
	words := regexp.MustCompile("`komodo ([a-z-]+)(?: ([a-z]+))?").FindAllStringSubmatch(block, -1)
	if len(words) == 0 {
		t.Fatal("the block names no command")
	}
	for _, w := range words {
		if w[1] == "--help" || findCommand(w[1]) != nil || (w[2] != "" && findCommand(w[1]+" "+w[2]) != nil) {
			continue
		}
		t.Errorf("the block names komodo %s, which the table lacks", strings.TrimSpace(w[1]+" "+w[2]))
	}
}

func TestSettingsMergeLeavesOtherKeysAndRemoveRestoresThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"permissions":{"allow":["Bash(ls)"],"ask":["Bash(rm *)"]},"hooks":{"PreToolUse":[{"matcher":"Edit",` +
		`"hooks":[{"type":"command","command":"x"}]}]},"model":"opus"}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range settingsEntries {
		if err := putSettings(path, e.id); err != nil {
			t.Fatal(err)
		}
		if st := settingsState(path, e.id); !st.exists || st.hash != sha([]byte(e.value)) {
			t.Fatalf("%s: state %+v", e.id, st)
		}
	}
	doc, _ := loadSettings(path)
	if allow, _ := doc.list("permissions.allow"); len(allow) != 2 {
		t.Fatalf("allow = %v", allow)
	}
	if pre, _ := doc.list("hooks.PreToolUse"); len(pre) != 1 {
		t.Fatalf("PreToolUse = %v", pre)
	}
	for _, e := range settingsEntries {
		if err := dropSettings(path, e.id); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(path)
	if strings.Join(strings.Fields(string(got)), "") != strings.ReplaceAll(orig, " ", "") &&
		!strings.Contains(string(got), `"ask"`) {
		t.Fatalf("after remove: %s", got)
	}
	doc, _ = loadSettings(path)
	if allow, _ := doc.list("permissions.allow"); len(allow) != 1 {
		t.Fatalf("allow after remove = %v", allow)
	}
	if pre, _ := doc.list("hooks.PreToolUse"); len(pre) != 1 {
		t.Fatalf("PreToolUse after remove = %v", pre)
	}
}

func TestSettingsRejectsAFileThatIsNotAnObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`[1]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := putSettings(path, settingsEntries[0].id); err == nil {
		t.Fatal("a non-object settings file was accepted")
	}
}

func TestUpdateMachineRereadsAfterAStaleRevision(t *testing.T) {
	home := t.TempDir()
	m, err := state.NewMachine(home, "cli")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	changed, err := updateMachine(m, state.KindInstall, func(b *state.InstallBody) bool {
		calls++
		if calls == 1 {
			// A second writer saves between this read and this save.
			if _, serr := state.SaveMachine(
				m,
				state.KindInstall,
				state.InstallBody{Entries: []state.InstallEntry{}},
				0,
			); serr != nil {
				t.Fatal(serr)
			}
		}
		b.Entries = []state.InstallEntry{}
		b.Selections.Hosts = []string{"claude"}
		return true
	})
	if err != nil || !changed || calls != 2 {
		t.Fatalf("changed %v, calls %d, err %v", changed, calls, err)
	}
	rec, _ := state.LoadMachine[state.InstallBody](m, state.KindInstall)
	if rec.Header.Rev != 2 || len(rec.Body.Selections.Hosts) != 1 {
		t.Fatalf("record = %+v", rec)
	}
}
