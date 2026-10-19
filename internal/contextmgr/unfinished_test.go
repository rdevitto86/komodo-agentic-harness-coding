package contextmgr

import (
	"io/fs"
	"slices"
	"testing"

	komodo "github.com/rdevitto86/komodo-agentic-harness-coding"
)

func TestBuilderTakesTheUnfinishedSlotInRowAndTemplate(t *testing.T) {
	if !slices.Contains(slotTable["builder"].optional, Unfinished) {
		t.Fatal("the builder row does not list the unfinished slot as optional")
	}
	raw, err := fs.ReadFile(komodo.Embedded, "embed/templates/builder.md")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplate("builder", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tpl.Slots, Unfinished) {
		t.Fatalf("the builder template has no {{unfinished}} hole: %v", tpl.Slots)
	}
}
