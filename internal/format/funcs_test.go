package format

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNumberFunc(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 12400: "12,400", -1234567: "-1,234,567"} {
		if got, err := fnN(in); err != nil || got != want {
			t.Errorf("n(%d) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, _ := fnN(int(12400)); got != "12,400" {
		t.Errorf("n(int) = %q", got)
	}
	if _, err := fnN("x"); err == nil {
		t.Error("n accepted a string")
	}
}

func TestPluralFunc(t *testing.T) {
	if got, _ := fnPlural(1, "group", "groups"); got != "group" {
		t.Errorf("plural(1) = %q", got)
	}
	if got, _ := fnPlural(int64(0), "group", "groups"); got != "groups" {
		t.Errorf("plural(0) = %q", got)
	}
	if got, _ := fnPlural(2, "group", "groups"); got != "groups" {
		t.Errorf("plural(2) = %q", got)
	}
}

func TestDurFunc(t *testing.T) {
	for in, want := range map[time.Duration]string{
		42 * time.Second:             "42s",
		65 * time.Minute:             "1h 5m",
		time.Hour:                    "1h",
		90 * time.Second:             "1m 30s",
		3*time.Hour + 20*time.Second: "3h",
		0:                            "0s",
		500 * time.Millisecond:       "0s",
		-2 * time.Minute:             "-2m",
		49*time.Hour + 5*time.Minute + 3*time.Second: "49h 5m",
	} {
		if got := fnDur(in); got != want {
			t.Errorf("dur(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestPathFunc(t *testing.T) {
	if got := fnPath(span("a/b.go")); got != "`a/b.go`" {
		t.Errorf("short path = %q", got)
	}
	long := "/Users/rad/komodo/ai/komodo-agentic-harness-coding/internal/format/format.go"
	got := unspan(fnPath(span(long)))
	if !strings.HasPrefix(got, "/Users/") || !strings.HasSuffix(got, "/format.go") || !strings.Contains(got, "…") {
		t.Errorf("fold = %q", got)
	}
	if len([]rune(got)) > 45 {
		t.Errorf("fold over 45 characters: %q", got)
	}
	if got := fnPath(span("verylongname/" + strings.Repeat("x", 60))); !strings.HasPrefix(got, "`verylongname/") {
		t.Errorf("two-segment path was folded: %q", got)
	}
}

func TestCodeFunc(t *testing.T) {
	if got, err := fnCode("over_limit"); err != nil || got != "`over_limit`" {
		t.Errorf("code = %q, %v", got, err)
	}
	if got, err := fnCode(span("usage")); err != nil || got != "`usage`" {
		t.Errorf("code of a span = %q, %v", got, err)
	}
	if _, err := fnCode("nope"); !errors.Is(err, ErrCodeUnknown) {
		t.Errorf("err = %v, want ErrCodeUnknown", err)
	}
}

func TestFuncMapHasSix(t *testing.T) {
	if len(funcs) != 6 {
		t.Fatalf("function map has %d entries, want 6", len(funcs))
	}
	for _, k := range []string{"code", "n", "plural", "dur", "path", "table"} {
		if _, ok := funcs[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
}

func TestTableShape(t *testing.T) {
	got, err := tableText(escapedTable(Table{Head: []string{"a", "b"}, Rows: [][]string{{"1", "x|y"}, {"2"}}, What: "rows"}))
	if err != nil {
		t.Fatal(err)
	}
	want := "| `a` | `b` |\n|---|---|\n| `1` | `x\\|y` |\n| `2` |  |"
	if got != want {
		t.Errorf("table:\n%s\nwant:\n%s", got, want)
	}
	if got, _ := tableText(Table{}); got != "" {
		t.Errorf("empty table = %q", got)
	}
}
