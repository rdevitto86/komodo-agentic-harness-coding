package task

import (
	"context"
	"strings"
	"testing"
)

func TestReviewerChecksSlotListsUncoveredAddedLines(t *testing.T) {
	rv := testRoles(t)["reviewer"]

	clean, err := rv.Slots(context.Background(), RoleInput{})
	if err != nil || strings.Contains(clean["checks"], "no test covers") {
		t.Fatalf("clean checks slot = %q, %v", clean["checks"], err)
	}

	got, err := rv.Slots(context.Background(), RoleInput{Uncovered: []string{"a/a.go:4-5", "b/b.go:9"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Every mechanical check passed", "no test covers", "a/a.go:4-5", "b/b.go:9"} {
		if !strings.Contains(got["checks"], want) {
			t.Errorf("checks slot lacks %q: %q", want, got["checks"])
		}
	}
}
