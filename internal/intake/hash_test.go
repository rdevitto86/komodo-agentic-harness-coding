package intake

import (
	"strings"
	"testing"
)

func TestCardHash(t *testing.T) {
	t.Parallel()

	hash := func(t *testing.T, text, group string) string {
		t.Helper()

		h, err := CardHash(text, Group{Name: group})
		if err != nil {
			t.Fatal(err)
		}

		return h
	}

	t.Run("the hash has the sha256 prefix and 64 hexadecimal characters", func(t *testing.T) {
		t.Parallel()

		h := hash(t, readPlanFixture(t), "tg-1")
		if !strings.HasPrefix(h, "sha256:") || len(h) != len("sha256:")+64 {
			t.Fatalf("hash %q", h)
		}
	})

	t.Run("one byte of the group's section moves the hash, a task's included", func(t *testing.T) {
		t.Parallel()

		text := readPlanFixture(t)
		base := hash(t, text, "tg-1")

		for _, edit := range [][2]string{
			{"points: 3", "points: 5"},
			{"Add the stale check", "Add the stale chec"},
			{"### tsk-log", "### tsk-lo"},
		} {
			if !strings.Contains(text, edit[0]) {
				t.Fatalf("fixture lacks %q", edit[0])
			}

			if hash(t, strings.Replace(text, edit[0], edit[1], 1), "tg-1") == base {
				t.Fatalf("edit %q left the hash", edit[0])
			}
		}
	})

	t.Run("a change in another group, or after the section, leaves the hash", func(t *testing.T) {
		t.Parallel()

		text := readPlanFixture(t)
		base := hash(t, text, "tg-1")

		other := strings.Replace(text, "Revoke it.", "Revoke it!", 1)
		if hash(t, other, "tg-1") != base || hash(t, other, "tg-2") == hash(t, text, "tg-2") {
			t.Fatal("tg-2 text changed the wrong hash")
		}

		header := strings.Replace(text, "goal: Add lease revocation", "goal: Add lease revocation now", 1)
		if hash(t, header, "tg-1") != base {
			t.Fatal("a header edit moved the hash")
		}

		if hash(t, text+"\n## tg-3\n", "tg-2") != hash(t, text, "tg-2") {
			t.Fatal("a group appended after tg-2 moved its hash")
		}
	})

	t.Run("line endings do not change the hash", func(t *testing.T) {
		t.Parallel()

		text := readPlanFixture(t)
		if hash(t, strings.ReplaceAll(text, "\n", "\r\n"), "tg-1") != hash(t, text, "tg-1") {
			t.Fatal("CRLF moved the hash")
		}
	})

	t.Run("a group with no section is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := CardHash(readPlanFixture(t), Group{Name: "tg-9"}); err == nil {
			t.Fatal("want an error")
		}
	})
}
