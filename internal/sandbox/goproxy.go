package sandbox

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// GoSetupAccess returns what a Go setup command may read and the variables it gets so the person's download cache serves as
// a file proxy ahead of the host proxy. Both are empty when the cache folder is missing or its path cannot be a proxy list item.
func GoSetupAccess(modules, hostProxy, hostPrivate string) ([]string, []string) {
	if modules == "" || !filepath.IsAbs(modules) {
		return nil, nil
	}

	dl := filepath.Join(modules, "cache", "download")
	if fi, err := os.Stat(dl); err != nil || !fi.IsDir() {
		return nil, nil
	}
	if strings.ContainsAny(dl, ",|:\n\r\x00") {
		return nil, nil
	}
	if hostProxy == "" {
		hostProxy = "https://proxy.golang.org"
	}

	// Private modules skip the public checksum database; the repo's go.sum still verifies them.
	return []string{dl}, []string{
		"GOPROXY=" + fileURL(dl) + "," + hostProxy,
		"GONOPROXY=",
		"GOPRIVATE=",
		"GONOSUMDB=" + hostPrivate,
		"GIT_TERMINAL_PROMPT=0",
	}
}

// fileURL returns the file URL of an absolute path, with a leading slash added for a drive path.
func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	return (&url.URL{Scheme: "file", Path: p}).String()
}
