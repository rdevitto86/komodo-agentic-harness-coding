//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/task"
)

// hostBackend stands in for the builder's sandbox: it runs each command on the host in the instance's worktree.
type hostBackend struct {
	spec    sandbox.Spec
	prepare int
	release int
}

func (h *hostBackend) Name() string { return "host" }
func (h *hostBackend) Check() error { return nil }

func (h *hostBackend) Prepare(_ context.Context, s sandbox.Spec) (sandbox.Instance, error) {
	h.spec, h.prepare = s, h.prepare+1
	return sandbox.Instance{ID: "i1", Group: s.Group, Backend: "host"}, nil
}

func (h *hostBackend) Command(
	ctx context.Context,
	_ sandbox.Instance,
	s sandbox.Spec,
	c sandbox.Command,
) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = s.Worktree
	return cmd, nil
}

func (h *hostBackend) Release(context.Context, sandbox.Instance) error { h.release++; return nil }

// The fail-first runner runs a command on the base commit's files in a networkless builder sandbox and reports its exit code.
func TestBaseRunnerRunsCommandsOnTheBaseCommit(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).
			CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q")
	write("base")
	run("add", "a.txt")
	run("commit", "-q", "-m", "base")
	write("head")

	be := &hostBackend{}
	boxes := sandbox.NewManager(map[string]sandbox.Backend{sandbox.RoleBuilder: be}, nil)
	home := t.TempDir()
	plan := task.SandboxPlan{Temp: filepath.Join(home, "tmp"), Build: filepath.Join(home, "build")}

	r, err := openBaseRunner(context.Background(), boxes, repo, "plan-x", plan)
	if err != nil {
		t.Fatal(err)
	}

	if code, err := r.Run(context.Background(), "grep -q base a.txt"); err != nil || code != 0 {
		t.Fatalf("a command that holds on the base: exit %d err %v", code, err)
	}
	if code, err := r.Run(context.Background(), "grep -q head a.txt"); err != nil || code == 0 {
		t.Fatalf("a command that holds only on the working tree: exit %d err %v", code, err)
	}
	if be.spec.Role != sandbox.RoleBuilder || be.spec.Network || !strings.Contains(be.spec.Worktree, "base-") {
		t.Fatalf("sandbox spec: %+v", be.spec)
	}

	if err := r.Close(); err != nil || be.release != 1 {
		t.Fatalf("close: %v released %d", err, be.release)
	}
}

// The planner's brief carries the planning standard that the template tells it to read.
func TestPlannerBriefCarriesThePlanningStandard(t *testing.T) {
	f := newPlannerFixture(
		t,
		`{"type":"system","subtype":"init","apiKeySource":"none","session_id":"s1"}`,
		"#sleep 2",
		`{"type":"result","subtype":"success","structured_output":{"files":[]},"total_cost_usd":0.1}`,
	)

	done := make(chan error, 1)

	go func() {
		_, err := f.kit.intake.Plan(context.Background(), intake.PlanRequest{Goal: "Add a thing", Root: f.kit.root})
		done <- err
	}()

	brief, err := os.ReadFile(filepath.Join(f.planFolder(t), "brief.md"))
	if err != nil {
		t.Fatal(err)
	}

	<-done

	if !strings.Contains(string(brief), "## Standards: planning") ||
		!strings.Contains(string(brief), "Order by need, not by habit") {
		t.Fatalf("brief lacks the planning standard:\n%s", brief)
	}
}
