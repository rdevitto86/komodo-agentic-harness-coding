package cli

import "testing"

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no verb", []string{"komodo"}, ExitUsage},
		{"unknown verb", []string{"komodo", "nope"}, ExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Run(c.args); got != c.want {
				t.Fatalf("Run(%q) = %d, want %d", c.args, got, c.want)
			}
		})
	}
}
