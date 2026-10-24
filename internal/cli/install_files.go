package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

const (
	beginPrefix = "<!-- komodo:begin min=" // beginPrefix starts the begin marker line of the Komodo block.
	importLine  = "@AGENTS.md"             // importLine is the single line install owns in CLAUDE.md.
)

// renderBlock builds the Komodo block text; an empty rules text leaves out the rules line.
func renderBlock(pin, bin, rules, style string) string {
	var b strings.Builder

	fmt.Fprintf(&b, BlockBegin+"\n", pin)
	b.WriteString("#### Komodo harness\n\n")
	b.WriteString(
		"- **You are the orchestrator.** Drive the harness only through `komodo <command>`; with no terminal, every reply " +
			"is the JSON envelope.\n",
	)
	fmt.Fprintf(&b, "- **Installed at:** `%s`.\n", bin)
	b.WriteString(
		"- **Find your commands:** run `komodo --help --json` first. It lists every command, flag and result schema, and " +
			"marks the terminal-only ones.\n",
	)
	b.WriteString(
		"- **Not yours:** the terminal-only commands (`install`, `update`, `config set`). They refuse without a terminal. " +
			"Hand the person the command.\n",
	)
	b.WriteString(
		"- **Wait without polling:** run `komodo status --wait` in the background. It returns when a group is blocked or " +
			"done, with the blocked group's question.\n",
	)
	b.WriteString(
		"- **Answer a blocked group:** `komodo run <id> --approve` grants what it asked for; `komodo run <id> --deny` " +
			"refuses it.\n",
	)
	b.WriteString("- **Report:** relay the envelope's `report` field verbatim.\n")

	if r := strings.TrimSpace(rules); r != "" {
		fmt.Fprintf(&b, "- **Rules:** %s\n", r)
	}
	if s := strings.TrimSpace(style); s != "" {
		fmt.Fprintf(&b, "\n%s\n", demoteHeadings(s))
	}

	b.WriteString(BlockEnd)
	return b.String()
}

// demoteHeadings moves every heading line 4 levels down, so it sits below the block's `####` heading.
func demoteHeadings(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if rest := strings.TrimLeft(l, "#"); rest != l && strings.HasPrefix(rest, " ") {
			lines[i] = "####" + l
		}
	}
	return strings.Join(lines, "\n")
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
		return Block{}, false, fmt.Errorf(
			"AGENTS.md has %d begin markers (lines %v) and %d end markers (lines %v): keep one begin marker before one "+
				"end marker",
			len(begins),
			begins,
			len(ends),
			ends,
		)
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
