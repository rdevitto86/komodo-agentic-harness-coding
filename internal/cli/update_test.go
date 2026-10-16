package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// release is a fake release server holding one signed release.
type release struct {
	srv      *httptest.Server
	pub      ed25519.PublicKey
	version  string
	bin      []byte
	badSum   bool // the manifest names a checksum the binary does not have
	badSig   bool // the signature does not match the manifest
	assetHit atomic.Int32
}

// newRelease starts a server for the release of version holding bin.
func newRelease(t *testing.T, version, bin string) *release {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := &release{pub: pub, version: version, bin: []byte(bin)}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/latest.json"):
			_, _ = w.Write(r.manifest())
		case strings.HasSuffix(req.URL.Path, "/latest.json.sig"):
			sig := ed25519.Sign(priv, r.manifest())
			if r.badSig {
				sig[0] ^= 0xff
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(sig) + "\n"))
		case strings.HasPrefix(req.URL.Path, "/asset/"):
			r.assetHit.Add(1)
			_, _ = w.Write(r.bin)
		default:
			http.NotFound(w, req)
		}
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

// manifest returns the release manifest bytes.
func (r *release) manifest() []byte {
	sum := checksum(r.bin)
	if r.badSum {
		sum = strings.Repeat("0", 64)
	}
	b, _ := json.Marshal(ReleaseManifest{Version: r.version, Assets: []Asset{
		{OS: "linux", Arch: "amd64", URL: r.srv.URL + "/asset/komodo", SHA256: sum},
	}})
	return b
}

// config points update at the fake server.
func (r *release) config() ReleaseConfig {
	return ReleaseConfig{Releases: r.srv.URL, Keys: KeySet{Current: r.pub}, GOOS: "linux", GOARCH: "amd64"}
}

// update runs komodo update with a terminal and the fake release.
func (h *harness) update(r *release, args ...string) (int, Envelope) {
	h.t.Helper()
	h.terminal = true
	h.host.Release = r.config()
	return h.envelope(append([]string{"update"}, args...)...)
}

// seedBin puts an old binary in bin/komodo.
func seedBin(t *testing.T, h *harness) string {
	t.Helper()
	p := filepath.Join(h.home, "bin", "komodo")
	mustWrite(t, p, "old binary")
	return p
}

// updateRecord reads update.json.
func updateRecord(t *testing.T, h *harness) (state.UpdateBody, error) {
	t.Helper()
	m, err := state.NewMachine(h.home, "cli")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := state.LoadMachine[state.UpdateBody](m, state.KindUpdate)
	return rec.Body, err
}

func TestUpdateSwapsTheBinaryAndRecordsIt(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	code, e := h.update(r)
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if got := readFile(t, bin); got != "new binary" {
		t.Fatalf("bin/komodo = %q", got)
	}
	if got := readFile(t, filepath.Join(h.home, "versions", "1.0.1", "komodo")); got != "new binary" {
		t.Fatalf("version copy = %q", got)
	}
	if d := dataOf[UpdateData](t, e); d.Action != "swapped" || d.To != "1.0.1" {
		t.Fatalf("data = %+v", d)
	}
	rec, err := updateRecord(t, h)
	if err != nil || rec.Latest != "1.0.1" || rec.Previous == "" {
		t.Fatalf("update.json = %+v, %v", rec, err)
	}
	for _, f := range []string{filepath.Join(h.home, "state", "update.lock"), filepath.Join(h.home, "versions", "1.0.1", "komodo.part")} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s was left behind", f)
		}
	}
}

func TestUpdateCheckRecordsAndChangesNothingElse(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	code, e := h.update(r, "--check")
	if d := dataOf[UpdateData](t, e); code != 0 || d.Action != "checked" || readFile(t, bin) != "old binary" || r.assetHit.Load() != 0 {
		t.Fatalf("exit %d, data %+v, downloads %d", code, d, r.assetHit.Load())
	}
	if rec, err := updateRecord(t, h); err != nil || rec.Latest != "1.0.1" {
		t.Fatalf("update.json = %+v, %v", rec, err)
	}
}

func TestUpdateWhenNotNewerDownloadsNothing(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.0-beta.6", "same")
	code, e := h.update(r)
	if d := dataOf[UpdateData](t, e); code != 0 || d.Action != "current" || readFile(t, bin) != "old binary" || r.assetHit.Load() != 0 {
		t.Fatalf("exit %d, data %+v", code, d)
	}
}

func TestUpdateBadChecksumKeepsTheOldBinary(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	r.badSum = true
	code, e := h.update(r)
	if code != 4 || e.Error == nil || !strings.Contains(e.Error.Reason, "checksum") || readFile(t, bin) != "old binary" {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	for _, f := range []string{"komodo", "komodo.part"} {
		if _, err := os.Stat(filepath.Join(h.home, "versions", "1.0.1", f)); err == nil {
			t.Errorf("versions/1.0.1/%s exists", f)
		}
	}
	if _, err := os.Stat(filepath.Join(h.home, "state", "update.lock")); err == nil {
		t.Error("the lock was not released")
	}
}

func TestUpdateBadSignatureKeepsTheOldBinaryAndRecordsNothing(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	r.badSig = true
	code, e := h.update(r)
	if code != 4 || e.Error == nil || !strings.Contains(e.Error.Reason, "signature") || readFile(t, bin) != "old binary" || r.assetHit.Load() != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, err := updateRecord(t, h); err == nil {
		t.Error("update.json was written for an unverified manifest")
	}
}

func TestUpdateLockFreshRefusesStaleIsTakenOver(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	lock := filepath.Join(h.home, "state", "update.lock")
	mustWrite(t, lock, "999")
	code, e := h.update(r)
	if code != 1 || e.Error.Code != "locked" || readFile(t, bin) != "old binary" {
		t.Fatalf("fresh lock: exit %d, %+v", code, e.Error)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if code, e = h.update(r); code != 0 || readFile(t, bin) != "new binary" {
		t.Fatalf("stale lock: exit %d, %+v", code, e.Error)
	}
}

func TestUpdateCleanupKeepsCurrentPreviousAndInUseVersions(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	if code, e := h.install(t.TempDir(), "--machine-only", "--no-path"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	for _, v := range []string{"0.9.0", "0.8.0", "0.7.0"} {
		mustWrite(t, filepath.Join(h.home, "versions", v, "komodo"), v)
	}
	h.host.InUse = func() ([]string, error) { return []string{"0.7.0"}, nil }
	r := newRelease(t, "1.0.1", "new binary")
	if code, e := h.update(r); code != 0 {
		t.Fatalf("update: exit %d, %+v", code, e.Error)
	}
	ents, _ := os.ReadDir(filepath.Join(h.home, "versions"))
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != "0.7.0,1.0.0-beta.6+dev,1.0.1" {
		t.Fatalf("versions = %v", got)
	}
	m, _ := state.NewMachine(h.home, "cli")
	rec, _ := state.LoadMachine[state.InstallBody](m, state.KindInstall)
	var binHash string
	for _, e := range rec.Body.Entries {
		if strings.HasSuffix(e.Target, filepath.Join("bin", "komodo")) {
			binHash = e.Hash
		}
	}
	if binHash != checksum([]byte("new binary")) {
		t.Fatalf("install.json still holds %q for bin/komodo", binHash)
	}
}

type fakeMover struct {
	bin   string
	count int
}

// MoveRuns records the binary it was asked to move runs onto.
func (f *fakeMover) MoveRuns(_ context.Context, bin string) (int, error) {
	f.bin = bin
	return f.count, nil
}

func TestUpdateRestartRunsUsesTheMover(t *testing.T) {
	h := newHarness(t)
	seedBin(t, h)
	mv := &fakeMover{count: 2}
	h.host.Runs = mv
	r := newRelease(t, "1.0.1", "new binary")
	code, e := h.update(r, "--restart-runs")
	if d := dataOf[UpdateData](t, e); code != 0 || d.RunsMoved != 2 || mv.bin != filepath.Join(h.home, "bin", "komodo") {
		t.Fatalf("exit %d, data %+v, bin %q", code, d, mv.bin)
	}
}

func TestUpdateRestartRunsWithoutAMoverWritesNothing(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	code, e := h.update(r, "--restart-runs")
	if code != 4 || !strings.Contains(e.Error.Reason, "not built yet") || readFile(t, bin) != "old binary" || r.assetHit.Load() != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, err := updateRecord(t, h); err == nil {
		t.Error("update.json was written")
	}
}

func TestUpdateDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "1.0.1", "new binary")
	if code, e := h.update(r, "--dry-run"); code != 0 || readFile(t, bin) != "old binary" || r.assetHit.Load() != 0 {
		t.Fatalf("exit %d, %+v", code, e.Error)
	}
	if _, err := updateRecord(t, h); err == nil {
		t.Error("update.json was written")
	}
}

func TestUpdateToAnOlderVersion(t *testing.T) {
	h := newHarness(t)
	bin := seedBin(t, h)
	r := newRelease(t, "0.9.0", "older binary")
	code, e := h.update(r, "--to", "0.9.0")
	if d := dataOf[UpdateData](t, e); code != 0 || d.Action != "swapped" || readFile(t, bin) != "older binary" {
		t.Fatalf("exit %d, data %+v, %+v", code, d, e.Error)
	}
	if rec, _ := updateRecord(t, h); rec.Latest != "0.9.0" {
		t.Fatalf("latest = %q", rec.Latest)
	}
	r2 := newRelease(t, "0.8.0", "x")
	if code, e = h.update(r2, "--to", "0.9.0"); code != 4 || !strings.Contains(e.Error.Reason, "0.8.0") {
		t.Fatalf("mismatched version: exit %d, %+v", code, e.Error)
	}
	_ = fmt.Sprint()
}

func TestLockIsStaleAfterTenMinutes(t *testing.T) {
	now := time.Now()
	if lockStale(now.Add(-9*time.Minute), now) || !lockStale(now.Add(-11*time.Minute), now) {
		t.Fatal("lock age boundary is wrong")
	}
}
