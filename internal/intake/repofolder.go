package intake

import "strings"

// RepoFolder returns the name of a repository's folder under the plans folder: every "/" of the repository id becomes "-"
// and a leading "-" is dropped, so github.com/acme/shop is github.com-acme-shop.
func RepoFolder(repoID string) string {
	return strings.TrimPrefix(strings.ReplaceAll(repoID, "/", "-"), "-")
}
