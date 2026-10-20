package task

import (
	"context"
	"os/exec"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/sandbox"
)

// hostGoEnv returns the person's Go proxy list and private-module patterns; a test replaces it.
var hostGoEnv = func(ctx context.Context) (string, string) {
	out, err := exec.CommandContext(ctx, "go", "env", "GOPROXY", "GOPRIVATE").Output()
	if err != nil {
		return "", ""
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		return "", ""
	}

	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
}

// moduleMisses are the setup output fragments that say a Go module could not be fetched.
var moduleMisses = []string{
	"terminal prompts disabled", "could not read Username", "404 Not Found", "410 Gone", "module lookup disabled",
}

// withGoSetupAccess gives the spec of a Go repo's setup command the person's download cache as a read-only file proxy; any
// other spec stays as it is.
func (m *Manager) withGoSetupAccess(ctx context.Context, spec sandbox.Spec) sandbox.Spec {
	if !spec.Network || m.c.Language != "go" {
		return spec
	}

	proxy, private := hostGoEnv(ctx)
	spec.SetupReadable, spec.SetupEnv = sandbox.GoSetupAccess(m.c.Plan.Modules, proxy, private)
	return spec
}

// setupFix returns the line that tells the person how to fill the download cache when a Go setup failed on a module it does
// not hold, or an empty string otherwise.
func (m *Manager) setupFix(spec sandbox.Spec, output string) string {
	if len(spec.SetupReadable) == 0 {
		return ""
	}

	for _, miss := range moduleMisses {
		if strings.Contains(output, miss) {
			return "run `go mod download` in the repo on this machine, then `komodo run " + m.spec.Group + "`"
		}
	}

	return ""
}
