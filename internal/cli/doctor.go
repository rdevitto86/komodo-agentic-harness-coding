package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// lowVerifiedPct is the share of verified findings under which a review rule is reported.
const lowVerifiedPct = 50

// DoctorData is the data of reply.doctor.
type DoctorData struct {
	Checks []Check `json:"checks"`
}

// Check is the result of one doctor check.
type Check struct {
	Name   string `json:"name"`   // install, sandbox, docker, links, block, home or rules
	Result string `json:"result"` // pass, fail, or skip when the check cannot run in this build
	Detail string `json:"detail"` // what was found
	Fix    string `json:"fix"`    // the command or change that resolves a failure; empty on pass
}

// networkFS lists the parts of file system type names that mean a network or shared disk.
var networkFS = []string{"nfs", "smb", "cifs", "afp", "webdav", "9p", "ceph", "afs", "sshfs"}

// checkInstall compares every file install owns with its recorded fingerprint, and looks for komodo on PATH.
func (in *installer) checkInstall(loadErr error) Check {
	if loadErr != nil {
		return fail("install", loadErr.Error(), "komodo install")
	}

	var problems []string
	edited := false
	if in.machineRev == 0 {
		problems = append(problems, "this machine has no install record")
	}
	if in.doRepo && !in.hasRepoManifest {
		problems = append(problems, "this repository has no install manifest")
	}

	for _, e := range append(append([]Entry{}, in.machineRec...), in.repoRec...) {
		switch d := in.disk(filepath.IsAbs(e.Target), e); {
		case !d.exists:
			problems = append(problems, e.Target+" is missing")
		case d.hash != e.Hash:
			problems = append(problems, e.Target+" was edited by hand")
			edited = true
		}
	}

	if _, err := in.host.lookPath("komodo"); err != nil {
		problems = append(problems, "komodo is not on PATH")
	}

	switch {
	case len(problems) == 0:
		return pass("install", fmt.Sprintf(
			"%d owned entries match install.json and komodo is on PATH",
			len(in.machineRec)+len(in.repoRec)),
		)
	case edited && len(problems) == 1:
		return fail("install", problems[0], "komodo install --force, or keep your edit")
	}

	return fail("install", strings.Join(problems, "; "), "komodo install")
}

// checkLinks probes the model links, records the probe, and passes when a required link works and a configured local link
// works.
func (in *installer) checkLinks(ctx context.Context) Check {
	if in.host.Links == nil {
		return Check{
			Name:   "links",
			Result: "skip",
			Detail: "the model client is not wired in this build, so the links were not probed",
		}
	}

	_, results, err := probeLinks(ctx, in.host, in.machine)
	addrErr := in.paidAddressProblem()
	var parts []string
	required, localBad := false, false

	for _, r := range results {
		if r.Link == llm.AnthropicAPI && addrErr != nil {
			continue
		}

		res := "ok"
		if !r.OK {
			if res = r.Reason; res == "" {
				res = "unavailable"
			}
		}
		parts = append(parts, string(r.Link)+": "+res)
		switch r.Link {
		case llm.ClaudeCLI, llm.AnthropicAPI:
			required = required || r.OK
		case llm.Local:
			localBad = !r.OK
		}
	}

	if addrErr != nil {
		parts = append(parts, "anthropic-api: off, "+addrErr.Error())
	}

	detail := strings.Join(parts, "; ")

	switch {
	case err != nil:
		return fail("links", "record the probe: "+err.Error(), "komodo doctor")
	case !required && addrErr != nil:
		return fail(
			"links",
			"no model link works. "+detail,
			"komodo config set providers.anthropic.url https://api.anthropic.com, or log in with claude auth login",
		)
	case !required:
		return fail(
			"links",
			"no model link works. "+detail,
			"log in with claude auth login, or set the paid Anthropic key",
		)
	case localBad:
		return fail(
			"links",
			"the local model link is configured but does not work. "+detail,
			"start the local model server, or remove the local link from the settings",
		)
	}

	return pass("links", detail)
}

// paidAddressProblem returns why the paid Anthropic address is refused, or nil when it is empty, fine or unreadable.
func (in *installer) paidAddressProblem() error {
	if in.cli.deps.Config == nil {
		return nil
	}

	it, err := in.cli.deps.Config.GetConfigItem("providers.anthropic.url")
	if err != nil {
		return nil
	}

	addr, _ := it.Value.(string)
	if addr == "" {
		return nil
	}

	return llm.CheckBaseURL(addr)
}

// checkBlock looks for exactly one begin marker and one end marker in the repository's AGENTS.md.
func (in *installer) checkBlock() Check {
	if in.root == "" {
		return pass("block", "outside a repository: not checked")
	}

	text, ok, err := readText(filepath.Join(in.root, "AGENTS.md"))
	if err != nil || !ok {
		return fail("block", "AGENTS.md is missing", "komodo install --repo-only")
	}

	b, found, err := findBlock(text)

	switch {
	case err != nil:
		return fail(
			"block",
			err.Error(),
			"fix the markers in AGENTS.md by hand, then komodo install --repo-only",
		)
	case !found:
		return fail("block", "AGENTS.md has no Komodo block", "komodo install --repo-only")
	}

	return pass("block", "one begin and one end marker, pin "+b.Min)
}

// checkHome fails on a network file system or a disk that does not honour file locks, where shared state would be unsafe.
func (in *installer) checkHome(ctx context.Context) Check {
	dir := in.home

	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}

	fix := "move the Komodo home to a local disk: set KOMODO_HOME to a local folder"
	fs, err := in.host.fsType(dir)

	if err != nil {
		return fail("home", "read the file system type of "+dir+": "+err.Error(), fix)
	}

	for _, n := range networkFS {
		if strings.Contains(strings.ToLower(fs), n) {
			return fail("home", dir+" is on a network file system ("+fs+")", fix)
		}
	}

	if err := lockHonoured(ctx, dir); err != nil {
		return fail("home", err.Error(), fix)
	}
	return pass("home", dir+" is on a local file system ("+fs+") and honours file locks")
}

// checkRules reports review rules whose findings were verified less than half the time.
func (in *installer) checkRules() Check {
	if in.host.Rules == nil {
		return pass("rules", "no review data to check")
	}

	all, err := in.host.Rules()
	if err != nil {
		return fail("rules", "read the review counters: "+err.Error(), "komodo doctor")
	}

	var low []string
	for _, p := range all {
		if p.Submitted > 0 && p.VerifiedPct() < lowVerifiedPct {
			low = append(low, fmt.Sprintf("%s (%s) %.0f%% of %d", p.Rule, p.Model, p.VerifiedPct(), p.Submitted))
		}
	}

	if len(low) > 0 {
		return fail(
			"rules",
			"review rules verified under 50% of the time: "+strings.Join(low, "; "),
			"review the rule text in the lens catalog",
		)
	}

	return pass("rules", fmt.Sprintf("%d review rules checked; none under 50%%", len(all)))
}

// handleDoctor runs every check and exits 3 when one fails; it only refreshes the provider probe.
func handleDoctor(c *Call) Result {
	ctx := context.Background()
	in := &installer{
		cli:       c.cli,
		host:      c.cli.deps.Host,
		root:      c.Repo.Root,
		common:    c.CommonDir,
		doMachine: true,
		doRepo:    c.Repo.Root != "",
	}

	var err error
	if in.home, err = c.cli.komodoHome(); err == nil {
		in.user, err = os.UserHomeDir()
	}
	if err == nil {
		in.machine, err = c.cli.machine()
	}
	if err != nil {
		return internalErr("find the install folders: " + err.Error())
	}

	loadErr := in.load()
	checks := []Check{
		in.checkInstall(loadErr),
		checkSandbox(ctx, in.host),
		checkDocker(ctx, in.host),
		in.checkLinks(ctx),
		in.checkBlock(),
		in.checkHome(ctx),
		in.checkRules(),
	}

	data := DoctorData{Checks: checks}
	rows := format.Table{Head: []string{"check", "result", "fix"}, What: "checks"}
	out := OutcomeOK

	for _, ck := range checks {
		rows.Rows = append(rows.Rows, []string{ck.Name, ck.Result, ck.Fix})
		if ck.Result == "fail" {
			out = OutcomeHeld
		}
	}

	return Result{
		Outcome: out,
		Shape:   "reply.doctor",
		Facts:   format.DoctorFacts{Checks: rows},
		Data:    data,
	}
}

// pass builds a passing check.
func pass(name, detail string) Check { return Check{Name: name, Result: "pass", Detail: detail} }

// fail builds a failing check.
func fail(name, detail, fix string) Check {
	return Check{Name: name, Result: "fail", Detail: detail, Fix: fix}
}

// checkSandbox runs a harmless command inside the operating-system sandbox.
func checkSandbox(ctx context.Context, h Host) Check {
	be := h.osSandbox()
	if err := runHarmless(ctx, be); err != nil {
		return fail(
			"sandbox", be.Name()+" could not run a command: "+err.Error(),
			"fix the cause above, or install Docker",
		)
	}
	return pass("sandbox", be.Name()+" ran a harmless command")
}

// runHarmless starts the true program inside a throwaway instance of the backend.
func runHarmless(ctx context.Context, be sandbox.Backend) error {
	if err := be.Check(); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "komodo-doctor-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}

	spec := sandbox.Spec{
		Group:    "doctor",
		Worktree: filepath.Join(tmp, "wt"),
		Writable: []string{filepath.Join(tmp, "tmp")},
		Role:     sandbox.RoleBuilder,
	}

	for _, d := range []string{spec.Worktree, spec.Writable[0]} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	inst, err := be.Prepare(ctx, spec)
	if err != nil {
		return err
	}

	defer func() { _ = be.Release(ctx, inst) }()

	prog, err := exec.LookPath("true")
	if err != nil {
		prog = "/usr/bin/true"
	}

	cmd, err := be.Command(
		ctx, inst, spec, sandbox.Command{Argv: []string{prog}, Timeout: 10 * time.Second},
	)
	if err != nil {
		return err
	}

	return cmd.Run()
}

// checkDocker looks for the Docker tools and asks the daemon; without Docker the Tester uses the operating-system sandbox.
func checkDocker(_ context.Context, h Host) Check {
	if _, err := h.lookPath("docker"); err != nil {
		return pass(
			"docker", "Docker is not installed; the Tester uses the operating-system sandbox and the run report says so",
		)
	}
	be := h.docker()
	if err := be.Check(); err != nil {
		return fail(
			"docker", err.Error(), "start Docker, or the Tester falls back to the operating-system sandbox",
		)
	}
	return pass("docker", "the Docker daemon answers")
}

// lockHonoured takes a lock on a probe file in dir and checks that a second taker is kept out.
func lockHonoured(ctx context.Context, dir string) error {
	path := filepath.Join(dir, fmt.Sprintf(".komodo-doctor-lock-%d", os.Getpid()))

	defer func() { _ = os.Remove(path) }()

	unlock, err := core.FileLock(ctx, path, time.Second)
	if err != nil {
		return fmt.Errorf("take a file lock in %s: %w", dir, err)
	}

	defer func() { _ = unlock() }()

	second, err := core.FileLock(ctx, path, 100*time.Millisecond)
	if err == nil {
		_ = second()
		return fmt.Errorf("%s does not honour file locks: two holders took the same lock", dir)
	}
	if !errors.Is(err, core.ErrLockWait) {
		return fmt.Errorf("test the file lock in %s: %w", dir, err)
	}

	return nil
}
