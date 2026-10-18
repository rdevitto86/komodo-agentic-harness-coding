package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// installed runs a full install into a fresh repository and returns the repository; komodo is then on PATH and docker is
// not.
func installed(t *testing.T, h *harness) string {
	t.Helper()
	repo := repoDir(t, "")
	if code, e := h.install(repo, "--hosts", "claude"); code != 0 {
		t.Fatalf("install: exit %d, %+v", code, e.Error)
	}
	h.host.LookPath = func(n string) (string, error) {
		if n == "docker" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + n, nil
	}
	return repo
}

// doctor runs komodo doctor in repo and returns the exit code and the checks by name.
func (h *harness) doctor(repo string) (int, map[string]Check) {
	h.t.Helper()
	code, e := h.envelope("-C", repo, "doctor")
	out := map[string]Check{}
	for _, c := range dataOf[DoctorData](h.t, e).Checks {
		out[c.Name] = c
	}
	return code, out
}

func TestDoctorPassesOnAHealthyMachine(t *testing.T) {
	h := newHarness(t)
	repo := installed(t, h)
	code, checks := h.doctor(repo)
	if code != 0 || len(checks) != 7 {
		t.Fatalf("exit %d, checks %+v", code, checks)
	}
	for n, c := range checks {
		if c.Result != "pass" || c.Fix != "" {
			t.Errorf("%s: %+v", n, c)
		}
	}
	if !strings.Contains(checks["docker"].Detail, "operating-system sandbox") {
		t.Errorf("docker detail = %q", checks["docker"].Detail)
	}
}

func TestDoctorRefreshesTheProviderProbe(t *testing.T) {
	h := newHarness(t)
	repo := installed(t, h)
	h.host.Links = fakeLinks{{Link: llm.ClaudeCLI, OK: true, Version: "9.9.9"}}
	h.doctor(repo)
	m, _ := state.NewMachine(h.home, "cli")
	rec, err := state.LoadMachine[state.ProvidersBody](m, state.KindProviders)
	if err != nil || !strings.Contains(string(rec.Body.Links["claude-cli"].Probe), "9.9.9") {
		t.Fatalf("providers.json = %+v, %v", rec.Body, err)
	}
}

func TestDoctorFailsWithAFixAndExitsThree(t *testing.T) {
	cases := map[string]func(h *harness, repo string){
		"sandbox": func(h *harness, _ string) {
			h.host.Sandbox = fakeBackend{
				name: "x",
				err:  errors.New("bwrap is not installed: install the bubblewrap package"),
			}
		},
		"docker": func(h *harness, _ string) {
			h.host.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
			h.host.Docker = fakeBackend{name: "docker", err: errors.New("the docker daemon does not answer: start Docker")}
		},
		"links": func(h *harness, _ string) {
			h.host.Links = fakeLinks{
				{Link: llm.ClaudeCLI, Reason: "claude is not logged in"},
				{Link: llm.AnthropicAPI, Reason: "no key"},
			}
		},
		"block": func(_ *harness, repo string) {
			mustWrite(t, filepath.Join(repo, "AGENTS.md"), "<!-- komodo:begin min=1.0.0 -->\nno end\n")
		},
		"install": func(h *harness, _ string) { _ = os.Remove(filepath.Join(h.home, "bin", "komodo")) },
		"home":    func(h *harness, _ string) { h.host.FSType = func(string) (string, error) { return "nfs", nil } },
		"rules": func(h *harness, _ string) {
			h.host.Rules = func() ([]agent.Precision, error) {
				return []agent.Precision{
					{Rule: "correctness.1", Model: "m", Submitted: 10, Verified: 4},
					{Rule: "style.2", Model: "m", Submitted: 10, Verified: 6},
				}, nil
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			repo := installed(t, h)
			mutate(h, repo)
			code, checks := h.doctor(repo)
			c := checks[name]
			if code != 3 || c.Result != "fail" || c.Fix == "" || c.Detail == "" {
				t.Fatalf("exit %d, %s = %+v", code, name, c)
			}
			for n, o := range checks {
				// A damaged block also leaves the owned AGENTS.md entry unmatched.
				if n != name && o.Result != "pass" && (name != "block" || n != "install") {
					t.Errorf("%s also failed: %+v", n, o)
				}
			}
		})
	}
}

func TestDoctorLocalLinkThatIsConfiguredMustWork(t *testing.T) {
	h := newHarness(t)
	repo := installed(t, h)
	h.host.Links = fakeLinks{{Link: llm.ClaudeCLI, OK: true}, {Link: llm.Local, Reason: "ollama does not answer"}}
	code, checks := h.doctor(repo)
	if code != 3 || checks["links"].Result != "fail" || !strings.Contains(checks["links"].Detail, "ollama") {
		t.Fatalf("exit %d, links = %+v", code, checks["links"])
	}
}

func TestDoctorReportsAHandEditedOwnedFileAndMissingInstall(t *testing.T) {
	h := newHarness(t)
	repo := installed(t, h)
	p := filepath.Join(repo, "AGENTS.md")
	mustWrite(t, p, strings.Replace(readFile(t, p), "#### Komodo harness", "#### Mine", 1))
	_, checks := h.doctor(repo)
	if c := checks["install"]; c.Result != "fail" || !strings.Contains(c.Detail, "AGENTS.md") {
		t.Fatalf("install = %+v", c)
	}
	h2 := newHarness(t)
	_, checks = h2.doctor(repoDir(t, ""))
	if c := checks["install"]; c.Result != "fail" || c.Fix != "komodo install" {
		t.Fatalf("no install: %+v", c)
	}
}

func TestDoctorOutsideARepositoryChecksTheMachineOnly(t *testing.T) {
	h := newHarness(t)
	installed(t, h)
	code, checks := h.doctor(t.TempDir())
	if code != 0 || checks["block"].Result != "pass" || !strings.Contains(checks["block"].Detail, "repository") {
		t.Fatalf("exit %d, block = %+v", code, checks["block"])
	}
}
