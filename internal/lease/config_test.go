package lease

import (
	"testing"
	"time"
)

func TestLockTimeout(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want time.Duration
	}{
		{"int", 5, 5 * time.Second},
		{"float from json", float64(7), 7 * time.Second},
		{"int64", int64(9), 9 * time.Second},
		{"zero falls back", 0, DefaultLockTimeout},
		{"negative falls back", -3, DefaultLockTimeout},
		{"wrong type falls back", "x", DefaultLockTimeout},
		{"nil falls back", nil, DefaultLockTimeout},
	}
	for _, c := range cases {
		if got := LockTimeout(c.in); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
