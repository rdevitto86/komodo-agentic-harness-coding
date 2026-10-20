package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestDockerSetupMountsTheDownloadCacheReadOnlyAndSetsTheProxy(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	goRepo(t, s)

	s.Network = true
	s.SetupReadable, s.SetupEnv = GoSetupAccess(hostCache(t), "https://proxy.golang.org", "example.com/*")
	dl := s.SetupReadable[0]

	m := NewManager(map[string]Backend{RoleTester: NewDocker(DockerOptions{})}, nil)
	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(calls().verb("create")[0], "-v", dl+":"+dl+":ro") < 0 {
		t.Fatalf("create lacks the read-only download cache: %v", calls().verb("create")[0])
	}

	exec := func() []string {
		t.Helper()

		cmd, err := m.Command(context.Background(), inst.ID, Command{Argv: []string{"go", "mod", "download"}})
		if err != nil {
			t.Fatal(err)
		}

		return cmd.Args
	}

	if args := exec(); indexOf(args, "-e", "GONOPROXY=") < 0 || indexOf(args, "-e", "GIT_TERMINAL_PROMPT=0") < 0 {
		t.Fatalf("the setup exec lacks the proxy variables: %v", args)
	}
	if err := m.EndSetup(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if args := exec(); strings.Contains(strings.Join(args, " "), "GOPROXY=") || indexOf(args, "-e", "GONOPROXY=") >= 0 {
		t.Fatalf("a later exec keeps the proxy variables: %v", args)
	}
}

func TestDockerWithoutSetupAccessMountsNoDownloadCache(t *testing.T) {
	calls := fakeDocker(t)
	s := testSpec(t, RoleTester)
	goRepo(t, s)

	s.Network = true

	m := NewManager(map[string]Backend{RoleTester: NewDocker(DockerOptions{})}, nil)
	if _, err := m.CreateSandboxInstance(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls().verb("create")[0], " "), "cache/download") {
		t.Fatalf("a download cache is mounted without a request: %v", calls().verb("create")[0])
	}
}
