package sandbox

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// GoSetupEnv returns the variables a Go setup command gets so the person's download cache, inside the module cache the spec
// already makes readable, serves as a file proxy ahead of the host proxy. It is nil when the cache folder is missing or its
// path cannot be a proxy list item.
func GoSetupEnv(modules, hostProxy, hostPrivate string) []string {
	if modules == "" || !filepath.IsAbs(modules) {
		return nil
	}

	dl := filepath.Join(modules, "cache", "download")
	if fi, err := os.Stat(dl); err != nil || !fi.IsDir() {
		return nil
	}
	if strings.ContainsAny(dl, ",|:\n\r\x00") {
		return nil
	}
	if hostProxy == "" {
		hostProxy = "https://proxy.golang.org"
	}

	// Private modules skip the public checksum database; the repo's go.sum still verifies them.
	return []string{
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
