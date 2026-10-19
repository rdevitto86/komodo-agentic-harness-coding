package cli

import (
	"slices"
	"testing"
)

func TestStatusFactsListTheErrorCountsByType(t *testing.T) {
	f := statusFacts(StatusData{Errors: []ErrorRow{
		{Type: "crash", Tier: "blocker", Count: 4},
		{Type: "timeout", Tier: "work", Count: 1},
	}})

	want := [][]string{{"crash", "blocker", "4"}, {"timeout", "work", "1"}}
	if !slices.EqualFunc(f.Errors.Rows, want, slices.Equal) {
		t.Fatalf("error rows %v, want %v", f.Errors.Rows, want)
	}
}
