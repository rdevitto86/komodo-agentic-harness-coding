package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PR is one pull request as the forge reports it.
type PR struct {
	URL     string
	HeadSHA string
}

// Forge runs the gh commands the Commit Handler needs; no agent holds its credentials.
type Forge interface {
	// FindPR returns the open pull request whose head is the branch.
	FindPR(ctx context.Context, head string) (PR, bool, error)
	// OpenPR opens a ready pull request from head into base, with the label.
	OpenPR(ctx context.Context, base, head, title, body, label string) (PR, error)
	// UpdatePR replaces the title and body of an open pull request.
	UpdatePR(ctx context.Context, url, title, body string) error
}

// GH is the Forge that runs the gh program in a repository folder, with the person's own login.
type GH struct {
	Dir string // folder gh runs in, a checkout of the repository
	Bin string // the program to run; empty means gh
}

// NewGH returns a Forge that runs gh in dir.
func NewGH(dir string) *GH { return &GH{Dir: dir} }

// run executes gh with the given standard input and returns its standard output.
func (f *GH) run(ctx context.Context, stdin string, args ...string) (string, error) {
	bin := f.Bin
	if bin == "" {
		bin = "gh"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = f.Dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// FindPR returns the open pull request whose head is the branch.
func (f *GH) FindPR(ctx context.Context, head string) (PR, bool, error) {
	out, err := f.run(ctx, "", "pr", "list", "--head", head, "--state", "open", "--json", "url,headRefOid", "--limit", "1")
	if err != nil {
		return PR{}, false, err
	}
	var list []struct {
		URL     string `json:"url"`
		HeadSHA string `json:"headRefOid"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return PR{}, false, fmt.Errorf("read gh pr list output: %w", err)
	}
	if len(list) == 0 {
		return PR{}, false, nil
	}
	return PR{URL: list[0].URL, HeadSHA: list[0].HeadSHA}, true, nil
}

// OpenPR makes the label when the repository lacks it, then opens a ready pull request.
func (f *GH) OpenPR(ctx context.Context, base, head, title, body, label string) (PR, error) {
	if _, err := f.run(ctx, "", "label", "create", label, "--force"); err != nil {
		return PR{}, err
	}
	out, err := f.run(ctx, body, "pr", "create", "--base", base, "--head", head, "--title", title, "--body-file", "-", "--label", label)
	if err != nil {
		return PR{}, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	url := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(url, "http") {
		return PR{}, fmt.Errorf("gh pr create printed no address: %q", out)
	}
	return PR{URL: url}, nil
}

// UpdatePR replaces the title and body of an open pull request.
func (f *GH) UpdatePR(ctx context.Context, url, title, body string) error {
	_, err := f.run(ctx, body, "pr", "edit", url, "--title", title, "--body-file", "-")
	return err
}
