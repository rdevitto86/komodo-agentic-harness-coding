package intake

import "testing"

func TestRepoFolder(t *testing.T) {
	for id, want := range map[string]string{
		"github.com/acme/shop":      "github.com-acme-shop",
		"/Users/dev/shop/.git":      "Users-dev-shop-.git",
		"already-flat":              "already-flat",
		"":                          "",
		"github.com/acme/shop-more": "github.com-acme-shop-more",
	} {
		if got := RepoFolder(id); got != want {
			t.Errorf("RepoFolder(%q) = %q, want %q", id, got, want)
		}
	}
}
