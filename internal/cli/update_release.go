package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
)

// defaultReleases is the address of the project's GitHub releases.
const defaultReleases = "https://github.com/rdevitto86/komodo-agentic-harness-coding/releases"

// manifestTimeout is the longest the release manifest fetch may take.
const manifestTimeout = time.Second

// downloadTimeout is the longest a binary download may take.
const downloadTimeout = 5 * time.Minute

// maxBinary caps the size of a downloaded binary.
const maxBinary = 512 << 20

// cdnHosts are the hosts GitHub redirects release downloads to.
var cdnHosts = []string{"objects.githubusercontent.com", "release-assets.githubusercontent.com"}

// releaseKeyCurrent and releaseKeyNext are base64 ed25519 public keys a release build sets with -ldflags -X.
var (
	releaseKeyCurrent string
	releaseKeyNext    string
)

// ReleaseManifest is latest.json: one release and how to verify its binaries.
type ReleaseManifest struct {
	Version string  `json:"version"` // semantic version of the release
	Assets  []Asset `json:"assets"`  // one per operating system and architecture
}

// Asset is the binary of one operating system and architecture.
type Asset struct {
	OS     string `json:"os"`     // linux or darwin
	Arch   string `json:"arch"`   // processor architecture
	URL    string `json:"url"`    // download address
	SHA256 string `json:"sha256"` // SHA-256 checksum of the binary, hexadecimal
}

// KeySet is the pair of public ed25519 keys compiled into the binary.
type KeySet struct {
	Current ed25519.PublicKey // signs today's releases
	Next    ed25519.PublicKey // lets a leaked key be rotated without breaking installed binaries
}

// ReleaseConfig says where update finds releases; a zero field uses the built-in value.
type ReleaseConfig struct {
	Releases string        // address of the releases folder, such as .../releases
	Keys     KeySet        // the public keys that sign releases
	Timeout  time.Duration // limit for the manifest fetch
	GOOS     string        // operating system of the wanted binary
	GOARCH   string        // processor architecture of the wanted binary
}

// base returns the folder holding latest.json: the latest release, or the release of version.
func (r ReleaseConfig) base(version string) string {
	root := strings.TrimRight(r.Releases, "/")
	if root == "" {
		root = defaultReleases
	}
	if version == "" {
		return root + "/latest/download"
	}
	return root + "/download/v" + version
}

// keys returns the configured keys, else the pair compiled in.
func (r ReleaseConfig) keys() KeySet {
	if len(r.Keys.Current) > 0 || len(r.Keys.Next) > 0 {
		return r.Keys
	}
	var k KeySet
	if b, err := base64.StdEncoding.DecodeString(releaseKeyCurrent); err == nil && len(b) == ed25519.PublicKeySize {
		k.Current = b
	}
	if b, err := base64.StdEncoding.DecodeString(releaseKeyNext); err == nil && len(b) == ed25519.PublicKeySize {
		k.Next = b
	}
	return k
}

// verify reports whether sig is a valid signature of msg under either key.
func (k KeySet) verify(msg, sig []byte) error {
	if len(k.Current) == 0 && len(k.Next) == 0 {
		return errors.New("no release signing key is compiled into this binary")
	}
	for _, key := range []ed25519.PublicKey{k.Current, k.Next} {
		if len(key) == ed25519.PublicKeySize && ed25519.Verify(key, msg, sig) {
			return nil
		}
	}
	return errors.New("the signature does not match a release key")
}

// asset returns the binary built for an operating system and architecture.
func (m ReleaseManifest) asset(goos, goarch string) (Asset, bool) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Asset{}, false
}

// platform returns the operating system and architecture update fetches a binary for.
func (r ReleaseConfig) platform() (string, string) {
	goos, goarch := r.GOOS, r.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

// fetchManifest downloads latest.json and its signature, checks the signature and returns the manifest.
func fetchManifest(ctx context.Context, c *core.Client, r ReleaseConfig, version string) (ReleaseManifest, error) {
	var m ReleaseManifest
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = manifestTimeout
	}
	base := r.base(version)
	body, err := httpGet(ctx, c, base+"/latest.json", timeout, nil)
	if err != nil {
		return m, fmt.Errorf("fetch latest.json: %w", err)
	}
	sigText, err := httpGet(ctx, c, base+"/latest.json.sig", timeout, nil)
	if err != nil {
		return m, fmt.Errorf("fetch latest.json.sig: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil {
		return m, fmt.Errorf("latest.json.sig is not base64: %w", err)
	}
	if err := r.keys().verify(body, sig); err != nil {
		return m, fmt.Errorf("verify latest.json: %w", err)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("read latest.json: %w", err)
	}
	if _, err := core.ParseSemVer(m.Version); err != nil {
		return m, fmt.Errorf("latest.json has no usable version: %w", err)
	}
	goos, goarch := r.platform()
	if _, ok := m.asset(goos, goarch); !ok {
		return m, fmt.Errorf("latest.json has no binary for %s/%s", goos, goarch)
	}
	return m, nil
}

// httpGet fetches one address under a time limit; hosts adds allowed hosts beside the address's own.
func httpGet(ctx context.Context, c *core.Client, address string, limit time.Duration, hosts []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	ep := core.Endpoint{Hosts: append([]string{u.Host}, append(hosts, cdnHosts...)...), Methods: []string{http.MethodGet}}
	resp, err := c.Do(ctx, ep, req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// checksum returns the lowercase hexadecimal SHA-256 of b.
func checksum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
