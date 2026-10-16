package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// lockStaleAfter is the age after which an update lock left by a crashed update is taken over.
const lockStaleAfter = 10 * time.Minute

// UpdateData is the data of reply.update.
type UpdateData struct {
	From      string `json:"from"`                 // the version that was running
	To        string `json:"to"`                   // the release found
	Action    string `json:"action"`               // swapped, current or checked
	RunsMoved int    `json:"runs_moved,omitempty"` // with --restart-runs: live runs moved onto the new binary
}

// updater holds what one update run needs.
type updater struct {
	cli                 *CLI
	host                Host
	home                string
	machine             *state.Machine
	client              *core.Client
	check, restart, dry bool
	to                  string
	running             string
	warn                func(f string, a ...any)
}

// handleUpdate replaces bin/komodo with the newest signed release, or reports what it would do.
func handleUpdate(c *Call) Result {
	u := &updater{cli: c.cli, host: c.cli.deps.Host, running: core.BinaryVersion()}
	u.check, u.restart, u.dry, u.to = flagBool(c, "check"), flagBool(c, "restart-runs"), c.Global.DryRun, flagString(c, "to")
	u.warn = func(f string, a ...any) { _, _ = fmt.Fprintf(c.cli.errOut, f, a...) }
	if u.to != "" {
		if _, err := core.ParseSemVer(u.to); err != nil {
			return usageErr(fmt.Sprintf("--to %q is not a version", u.to), helpFix("update"))
		}
	}
	if u.restart && u.host.Runs == nil {
		return notBuilt("update --restart-runs, which needs the engine to move live runs,")
	}
	var err error
	if u.home, err = c.cli.komodoHome(); err == nil {
		u.machine, err = c.cli.machine()
	}
	if err == nil {
		u.client, err = core.NewClient(core.Options{MaxBody: maxBinary})
	}
	if err != nil {
		return internalErr("prepare the update: " + err.Error())
	}
	return u.run(context.Background())
}

// updateFail builds the failure reply of a step with the fix that resolves it.
func updateFail(err error, fix string) Result {
	return errResult(OutcomeInternal, format.Internal, err.Error(), fix)
}

// run takes the steps of one update: check, record, claim, download, verify, swap and clean up.
func (u *updater) run(ctx context.Context) Result {
	man, err := fetchManifest(ctx, u.client, u.host.Release, u.to)
	if err != nil {
		return updateFail(err, "check the network, then run komodo update again")
	}
	if u.to != "" && semverCompare(man.Version, u.to) != 0 {
		return updateFail(fmt.Errorf("the release holds version %s, not %s", man.Version, u.to), "run komodo update --check to see the newest version")
	}
	data := UpdateData{From: strings.SplitN(u.running, "+", 2)[0], To: man.Version, Action: "checked"}
	if !u.dry {
		if err := u.record(man.Version); err != nil {
			return updateFail(err, "run komodo update again")
		}
	}
	cmp := semverCompare(man.Version, u.running)
	if u.check || u.dry {
		return updateResult(data)
	}
	if (u.to == "" && cmp <= 0) || (u.to != "" && cmp == 0) {
		data.Action = "current"
		return updateResult(data)
	}
	release, res := u.claim()
	if res != nil {
		return *res
	}
	defer release()
	asset, _ := man.asset(u.host.Release.platform())
	dir := filepath.Join(u.home, "versions", man.Version)
	if err := u.download(ctx, asset, dir); err != nil {
		return updateFail(err, "run komodo update again")
	}
	previous := u.previous()
	if err := u.swap(dir); err != nil {
		return updateFail(err, "run komodo update again")
	}
	u.recordSwap(man, previous, dir)
	u.cleanup(man.Version, previous)
	data.Action = "swapped"
	if u.restart {
		n, err := u.host.Runs.MoveRuns(ctx, filepath.Join(u.home, "bin", "komodo"))
		if err != nil {
			return updateFail(fmt.Errorf("the binary was updated, but moving live runs failed: %w", err), "komodo stop each run, then komodo run it again")
		}
		data.RunsMoved = n
	}
	return updateResult(data)
}

// updateResult builds the reply from the finished data.
func updateResult(d UpdateData) Result {
	return Result{Shape: "reply.update", Facts: format.UpdateFacts{From: d.From, To: d.To, Action: d.Action}, Data: d}
}

// semverCompare orders two versions; an unparsable one sorts lowest.
func semverCompare(a, b string) int {
	x, errA := core.ParseSemVer(a)
	y, errB := core.ParseSemVer(b)
	switch {
	case errA != nil && errB != nil:
		return 0
	case errA != nil:
		return -1
	case errB != nil:
		return 1
	}
	return x.Compare(y)
}

// record keeps the newest version seen in update.json.
func (u *updater) record(version string) error {
	_, err := updateMachine(u.machine, state.KindUpdate, func(b *state.UpdateBody) bool {
		if semverCompare(version, b.Latest) > 0 || b.Latest == "" {
			b.Latest = version
		}
		b.CheckedAt = u.host.now()
		return true
	})
	if err != nil {
		return fmt.Errorf("write update.json: %w", err)
	}
	return nil
}

// lockStale reports whether a lock last touched at modified is old enough to take over.
func lockStale(modified, now time.Time) bool { return now.Sub(modified) > lockStaleAfter }

// claim creates update.lock exclusively; a lock younger than ten minutes means another update is running.
func (u *updater) claim() (func(), *Result) {
	path := filepath.Join(u.home, "state", "update.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r := internalErr("create the state folder: " + err.Error())
		return nil, &r
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			r := internalErr("claim update.lock: " + err.Error())
			return nil, &r
		}
		fi, serr := os.Stat(path)
		if serr == nil && !lockStale(fi.ModTime(), u.host.now()) {
			r := refuse(format.Locked, "another komodo update is running", "wait for it to finish, then retry")
			return nil, &r
		}
		_ = os.Remove(path)
	}
	r := refuse(format.Locked, "another komodo update holds update.lock", "wait for it to finish, then retry")
	return nil, &r
}

// download fetches the asset to komodo.part, checks its SHA-256 and renames it into place.
func (u *updater) download(ctx context.Context, a Asset, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	part := filepath.Join(dir, "komodo.part")
	body, err := httpGet(ctx, u.client, a.URL, downloadTimeout, nil)
	if err != nil {
		return fmt.Errorf("download the binary: %w", err)
	}
	if err := writeSynced(part, body); err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("write %s: %w", part, err)
	}
	if got := checksum(body); !strings.EqualFold(got, a.SHA256) {
		_ = os.Remove(part)
		return fmt.Errorf("the downloaded binary has checksum %s, not the %s the manifest names", got, a.SHA256)
	}
	if err := os.Rename(part, filepath.Join(dir, "komodo")); err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("move the binary into place: %w", err)
	}
	return nil
}

// writeSynced writes a file readable and executable by its owner and flushes it to disk.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// swap replaces bin/komodo with a copy of the verified binary, atomically.
func (u *updater) swap(dir string) error {
	src, err := os.Open(filepath.Join(dir, "komodo"))
	if err != nil {
		return fmt.Errorf("open the new binary: %w", err)
	}
	defer func() { _ = src.Close() }()
	bin := filepath.Join(u.home, "bin", "komodo")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(bin), err)
	}
	return core.WriteAtomic(bin, 0o755, func(w io.Writer) error {
		_, err := io.Copy(w, src)
		return err
	})
}

// previous returns the version folder name bin/komodo was copied from, as install.json records it.
func (u *updater) previous() string {
	rec, err := state.LoadMachine[state.InstallBody](u.machine, state.KindInstall)
	if err == nil {
		for _, e := range rec.Body.Entries {
			if e.Step == stepBinary && filepath.Base(filepath.Dir(filepath.Dir(e.Target))) == "versions" {
				return filepath.Base(filepath.Dir(e.Target))
			}
		}
	}
	return u.running
}

// recordSwap stores the previous version in update.json and the new binary in install.json.
func (u *updater) recordSwap(man ReleaseManifest, previous, dir string) {
	if previous != man.Version {
		if _, err := updateMachine(u.machine, state.KindUpdate, func(b *state.UpdateBody) bool {
			b.Previous = previous
			return true
		}); err != nil {
			u.warn("Warning: write update.json: %v\n", err)
		}
	}
	bin := filepath.Join(u.home, "bin", "komodo")
	asset, _ := man.asset(u.host.Release.platform())
	ver := filepath.Join(dir, "komodo")
	if _, err := updateMachine(u.machine, state.KindInstall, func(b *state.InstallBody) bool {
		found := false
		var kept []state.InstallEntry
		for _, e := range b.Entries {
			switch {
			case e.Target == bin:
				e.Hash, found = strings.ToLower(asset.SHA256), true
			case e.Step == stepBinary && filepath.Base(filepath.Dir(filepath.Dir(e.Target))) == "versions":
				continue
			}
			kept = append(kept, e)
		}
		if !found {
			return false
		}
		b.Entries = append(kept, state.InstallEntry{Target: ver, Hash: strings.ToLower(asset.SHA256), Step: stepBinary})
		return true
	}); err != nil {
		u.warn("Warning: write install.json: %v\n", err)
	}
}

// cleanup deletes version folders that are not the current, the previous or one an unfinished task group names.
func (u *updater) cleanup(current, previous string) {
	keep := map[string]bool{current: true, previous: true}
	if u.host.InUse != nil {
		used, err := u.host.InUse()
		if err != nil {
			u.warn("Warning: old versions were kept: read the versions in use: %v\n", err)
			return
		}
		for _, v := range used {
			keep[v] = true
		}
	}
	ents, err := os.ReadDir(filepath.Join(u.home, "versions"))
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() && !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(u.home, "versions", e.Name())); err != nil {
				u.warn("Warning: remove old version %s: %v\n", e.Name(), err)
			}
		}
	}
}
