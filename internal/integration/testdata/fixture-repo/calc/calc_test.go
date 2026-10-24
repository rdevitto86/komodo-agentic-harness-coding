package calc

import "testing"

func TestAdd(t *testing.T) {
	for _, c := range []struct{ a, b, want int }{{1, 2, 3}, {0, 0, 0}, {-1, 1, 0}} {
		if got := Add(c.a, c.b); got != c.want {
			t.Fatalf("Add(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
