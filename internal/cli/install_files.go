package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// beginPrefix starts the begin marker line of the Komodo block.
const beginPrefix = "<!-- komodo:begin min="

// importLine is the single line install owns in CLAUDE.md.
const importLine = "@AGENTS.md"

// guardCommand is the command of the hook entry install owns.
const guardCommand = "komodo internal guard"

// renderBlock builds the Komodo block text; an empty rules text leaves out the rules line.
func renderBlock(pin, bin, rules, style string) string {
	var b strings.Builder
	fmt.Fprintf(&b, BlockBegin+"\n", pin)
	b.WriteString("#### Komodo harness\n\n")
	b.WriteString("- **You are the orchestrator.** Drive the harness only through `komodo <command>`; with no terminal, every reply is the JSON envelope.\n")
	fmt.Fprintf(&b, "- **Installed at:** `%s`.\n", bin)
	b.WriteString("- **Find your commands:** run `komodo --help --json` first. It lists every command, flag and result schema, and marks the terminal-only ones.\n")
	b.WriteString("- **Not yours:** the terminal-only commands (`install`, `update`, `config set`). They refuse without a terminal. Hand the person the command.\n")
	b.WriteString("- **Wait without polling:** run `komodo status --wait` in the background. It returns when a group is blocked or done, with the blocked group's question.\n")
	b.WriteString("- **Answer a blocked group:** `komodo run <id> --approve` grants what it asked for; `komodo run <id> --deny` refuses it.\n")
	b.WriteString("- **Report:** relay the envelope's `report` field verbatim.\n")
	if r := strings.TrimSpace(rules); r != "" {
		fmt.Fprintf(&b, "- **Rules:** %s\n", r)
	}
	if s := strings.TrimSpace(style); s != "" {
		b.WriteString(s + "\n")
	}
	b.WriteString(BlockEnd)
	return b.String()
}

// findBlock locates the Komodo block; a missing, repeated or misordered marker is an error that names the lines.
func findBlock(text string) (Block, bool, error) {
	var begins, ends []int
	var starts []int
	off := 0
	for i, line := range strings.SplitAfter(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, beginPrefix) && strings.HasSuffix(t, "-->"):
			begins = append(begins, i+1)
			starts = append(starts, off, off+len(strings.TrimRight(line, "\r\n")))
		case t == BlockEnd:
			ends = append(ends, i+1)
			starts = append(starts, off, off+len(strings.TrimRight(line, "\r\n")))
		}
		off += len(line)
	}
	switch {
	case len(begins) == 0 && len(ends) == 0:
		return Block{}, false, nil
	case len(begins) != 1 || len(ends) != 1 || begins[0] > ends[0]:
		return Block{}, false, fmt.Errorf("AGENTS.md has %d begin markers (lines %v) and %d end markers (lines %v): keep one begin marker before one end marker", len(begins), begins, len(ends), ends)
	}
	// starts holds, in file order, the begin marker's start and the end marker's end.
	b := Block{Start: starts[0], End: starts[3]}
	line := strings.TrimSpace(text[b.Start:starts[1]])
	b.Min = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, beginPrefix), "-->"))
	return b, true, nil
}

// putBlock replaces the Komodo block in text, or appends it when there is none.
func putBlock(text, block string) (string, error) {
	b, ok, err := findBlock(text)
	if err != nil {
		return "", err
	}
	switch {
	case ok:
		return text[:b.Start] + block + text[b.End:], nil
	case strings.TrimSpace(text) == "":
		return block + "\n", nil
	case strings.HasSuffix(text, "\n"):
		return text + "\n" + block + "\n", nil
	}
	return text + "\n\n" + block + "\n", nil
}

// dropBlock removes the Komodo block and the blank line install put before it.
func dropBlock(text string) (string, error) {
	b, ok, err := findBlock(text)
	if err != nil || !ok {
		return text, err
	}
	head, tail := text[:b.Start], strings.TrimPrefix(text[b.End:], "\n")
	if strings.HasSuffix(head, "\n\n") {
		head = head[:len(head)-1]
	}
	return head + tail, nil
}

// blockHash returns the fingerprint of the block found in the file, or false when there is none.
func blockHash(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	blk, ok, err := findBlock(string(b))
	if err != nil || !ok {
		return "", false
	}
	return sha([]byte(string(b)[blk.Start:blk.End])), true
}

// writeText writes a text file atomically, creating its folder.
func writeText(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create folder of %s: %w", path, err)
	}
	return core.WriteFileAtomic(path, []byte(text), 0o644)
}

// readText reads a text file; an absent file is "" with exists false.
func readText(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return string(b), err == nil, err
}

// hasImport reports whether CLAUDE.md text holds the import line on a line of its own.
func hasImport(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == importLine {
			return true
		}
	}
	return false
}

// addImport appends the import line to CLAUDE.md text that lacks it.
func addImport(text string) string {
	if hasImport(text) {
		return text
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + importLine + "\n"
}

// dropImport removes the import line from CLAUDE.md text.
func dropImport(text string) string {
	var keep []string
	for _, l := range strings.SplitAfter(text, "\n") {
		if strings.TrimSpace(l) != importLine {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "")
}

// settingsEntry is one entry install owns in .claude/settings.json.
type settingsEntry struct {
	id    string // the manifest name of the entry
	list  string // the dotted path of the list that holds it
	value string // the entry as compact JSON
}

// settingsEntries are the entries step 6 owns, in write order.
var settingsEntries = []settingsEntry{
	{"permissions.allow Bash(komodo *)", "permissions.allow", `"Bash(komodo *)"`},
	{"permissions.deny Bash(komodo config set*)", "permissions.deny", `"Bash(komodo config set*)"`},
	{"permissions.deny Bash(komodo install*)", "permissions.deny", `"Bash(komodo install*)"`},
	{"permissions.deny Bash(komodo update*)", "permissions.deny", `"Bash(komodo update*)"`},
	{"hooks.PreToolUse " + guardCommand, "hooks.PreToolUse",
		`{"matcher":"Bash|Edit|Write","hooks":[{"type":"command","command":"` + guardCommand + `","onFailure":"block"}]}`},
}

// settingsEntryOf returns the owned entry with the manifest name id.
func settingsEntryOf(id string) (settingsEntry, bool) {
	for _, e := range settingsEntries {
		if e.id == id {
			return e, true
		}
	}
	return settingsEntry{}, false
}

// settingsDoc is a settings file with its values kept as written.
type settingsDoc map[string]json.RawMessage

// loadSettings reads a settings file; an absent file is an empty document.
func loadSettings(path string) (settingsDoc, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settingsDoc{}, nil
	}
	if err != nil {
		return nil, err
	}
	doc := settingsDoc{}
	if len(bytes.TrimSpace(b)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", path, err)
	}
	return doc, nil
}

// list returns the list at a two-part dotted path; a missing list is empty.
func (d settingsDoc) list(path string) ([]json.RawMessage, error) {
	outer, inner, _ := strings.Cut(path, ".")
	obj := map[string]json.RawMessage{}
	if raw, ok := d[outer]; ok {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("%s is not an object: %w", outer, err)
		}
	}
	var l []json.RawMessage
	if raw, ok := obj[inner]; ok {
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("%s is not a list: %w", path, err)
		}
	}
	return l, nil
}

// setList stores a list at a dotted path; an empty list removes the key and any parent left empty.
func (d settingsDoc) setList(path string, l []json.RawMessage) error {
	outer, inner, _ := strings.Cut(path, ".")
	obj := map[string]json.RawMessage{}
	if raw, ok := d[outer]; ok {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("%s is not an object: %w", outer, err)
		}
	}
	if len(l) == 0 {
		delete(obj, inner)
	} else {
		b, _ := json.Marshal(l)
		obj[inner] = b
	}
	if len(obj) == 0 {
		delete(d, outer)
		return nil
	}
	b, _ := json.Marshal(obj)
	d[outer] = b
	return nil
}

// compact returns the compact form of a JSON value.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// find returns the index of the list element that is this entry: an equal value, or for the hook one that runs the guard command.
func (e settingsEntry) find(l []json.RawMessage) int {
	for i, el := range l {
		if compact(el) == e.value {
			return i
		}
		if e.list == "hooks.PreToolUse" && runsGuard(el) {
			return i
		}
	}
	return -1
}

// runsGuard reports whether a hook element runs the guard command.
func runsGuard(el json.RawMessage) bool {
	var h struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(el, &h) != nil {
		return false
	}
	for _, x := range h.Hooks {
		if x.Command == guardCommand {
			return true
		}
	}
	return false
}

// settingsState reports whether the entry is in the file and the fingerprint of what is there.
func settingsState(path, id string) diskState {
	e, ok := settingsEntryOf(id)
	if !ok {
		return diskState{}
	}
	doc, err := loadSettings(path)
	if err != nil {
		return diskState{}
	}
	l, err := doc.list(e.list)
	if err != nil {
		return diskState{}
	}
	i := e.find(l)
	if i < 0 {
		return diskState{}
	}
	return diskState{exists: true, hash: sha([]byte(compact(l[i])))}
}

// saveSettings writes the document, or removes the file when it holds nothing.
func saveSettings(path string, doc settingsDoc) error {
	if len(doc) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = os.Remove(filepath.Dir(path))
		return nil
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeText(path, string(b)+"\n")
}

// putSettings adds the entry to the settings file, replacing a changed copy of the hook.
func putSettings(path, id string) error {
	e, ok := settingsEntryOf(id)
	if !ok {
		return fmt.Errorf("unknown settings entry %q", id)
	}
	doc, err := loadSettings(path)
	if err != nil {
		return err
	}
	l, err := doc.list(e.list)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if i := e.find(l); i >= 0 {
		l[i] = json.RawMessage(e.value)
	} else {
		l = append(l, json.RawMessage(e.value))
	}
	if err := doc.setList(e.list, l); err != nil {
		return err
	}
	return saveSettings(path, doc)
}

// dropSettings removes the entry from the settings file.
func dropSettings(path, id string) error {
	e, ok := settingsEntryOf(id)
	if !ok {
		return fmt.Errorf("unknown settings entry %q", id)
	}
	doc, err := loadSettings(path)
	if err != nil {
		return err
	}
	l, err := doc.list(e.list)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	i := e.find(l)
	if i < 0 {
		return nil
	}
	if err := doc.setList(e.list, append(l[:i:i], l[i+1:]...)); err != nil {
		return err
	}
	return saveSettings(path, doc)
}
