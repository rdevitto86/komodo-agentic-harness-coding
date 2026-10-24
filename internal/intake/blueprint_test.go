package intake

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

// planIDShape matches a generated plan id.
var planIDShape = regexp.MustCompile(`^plan-[0-9a-f]{6}$`)

func TestBlueprint(t *testing.T) {
	t.Parallel()

	t.Run("writes and loads the same blueprint", func(t *testing.T) {
		t.Parallel()

		home := t.TempDir()
		b, _ := Parse(readPlanFixture(t))
		b.Repo = "repo-1"

		if err := WriteBlueprint(home, b); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(filepath.Join(home, "plans", "repo-1", "plan-7f3a9c", "blueprint.json")); err != nil {
			t.Fatal(err)
		}

		got, err := LoadBlueprint(home, "repo-1", "plan-7f3a9c")
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(got, b) {
			t.Fatalf("loaded %+v, want %+v", got, b)
		}
	})

	t.Run("a missing blueprint is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := LoadBlueprint(t.TempDir(), "r", "plan-000000"); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a corrupt blueprint is an error", func(t *testing.T) {
		t.Parallel()

		home := t.TempDir()
		dir := PlanDir(home, "r", "plan-000000")

		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "blueprint.json"), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := LoadBlueprint(home, "r", "plan-000000"); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a blueprint without a plan id is refused", func(t *testing.T) {
		t.Parallel()

		if err := WriteBlueprint(t.TempDir(), Blueprint{Repo: "r"}); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a blueprint without a repo id is refused", func(t *testing.T) {
		t.Parallel()

		if err := WriteBlueprint(t.TempDir(), Blueprint{Plan: "plan-aaaaaa"}); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("an unwritable home is an error", func(t *testing.T) {
		t.Parallel()

		file := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}

		if err := WriteBlueprint(file, Blueprint{Plan: "plan-aaaaaa", Repo: "r"}); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestNewPlanID(t *testing.T) {
	t.Parallel()

	t.Run("is plan- and 6 lowercase hex", func(t *testing.T) {
		t.Parallel()

		id, err := NewPlanID(nil)
		if err != nil || !planIDShape.MatchString(id) {
			t.Fatalf("id = %q, err = %v", id, err)
		}
	})

	t.Run("draws again when the id exists", func(t *testing.T) {
		t.Parallel()

		calls := 0

		id, err := NewPlanID(func(string) bool { calls++; return calls < 3 })
		if err != nil || calls != 3 || !planIDShape.MatchString(id) {
			t.Fatalf("id = %q, calls = %d, err = %v", id, calls, err)
		}
	})

	t.Run("gives up when every id exists", func(t *testing.T) {
		t.Parallel()

		if _, err := NewPlanID(func(string) bool { return true }); !errors.Is(err, ErrPlanIDTaken) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a failing random source is an error", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("no entropy")
		if _, err := newPlanID(nil, func([]byte) (int, error) { return 0, boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}
