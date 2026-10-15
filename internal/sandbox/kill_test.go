package sandbox

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestParseProcs(t *testing.T) {
	out := "  101   100   100 /bin/sh -c sleep 5 KOMODO_SANDBOX_CMD=abc HOME=/x\n\n 7 1 7 launchd\nnot a row\n"
	got := parseProcs(out)
	if len(got) != 2 || got[0].pid != 101 || got[0].ppid != 100 || got[0].pgid != 100 || !strings.Contains(got[0].line, "KOMODO_SANDBOX_CMD=abc") || got[1].pid != 7 {
		t.Fatalf("got %+v", got)
	}
}

func TestSelectVictimsCatchesEveryWayOutOfTheTree(t *testing.T) {
	const marker = "KOMODO_SANDBOX_CMD=abc"
	table := []procInfo{
		{100, 1, 100, "root"},
		{101, 100, 100, "child"},
		{102, 101, 102, "grandchild in its own group"},
		{103, 1, 100, "orphan still in the group"},
		{200, 1, 200, "setsid orphan " + marker + " HOME=/x"},
		{201, 200, 200, "its child"},
		{300, 1, 300, "unrelated"},
		{301, 300, 300, "unrelated child"},
		{400, 1, 400, "another command KOMODO_SANDBOX_CMD=other"},
	}
	got := selectVictims(table, 100, marker)
	sort.Ints(got)
	if want := []int{100, 101, 102, 103, 200, 201}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestWaitReleasesTheTimeoutContext(t *testing.T) {
	putFake(t, "bwrap", "exit 0\n")
	bw := NewBubblewrap(BubblewrapOptions{GOOS: "linux", Home: "/home/dev"})
	s := testSpec(t, RoleBuilder)
	inst, _ := bw.Prepare(context.Background(), s)
	count := func() int {
		n := 0
		releases.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	before := count()
	cmd, err := bw.Command(context.Background(), inst, s, Command{Argv: []string{"true"}, Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if count() != before+1 {
		t.Fatalf("a command with a timeout must hold a release: %d -> %d", before, count())
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := Wait(cmd); err != nil {
		t.Fatal(err)
	}
	if count() != before {
		t.Fatalf("Wait must release the timeout: %d -> %d", before, count())
	}
	if err := Wait(cmd); err == nil {
		t.Log("a second Wait reports the usual error and must not panic")
	}
}

func TestDockerCancelKillsTheProcessInsideTheContainer(t *testing.T) {
	calls := fakeDocker(t)
	t.Setenv("FAKE_DOCKER_EXEC_SLEEP", "1")
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	inst, err := d.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := d.Command(ctx, inst, s, Command{Argv: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	_ = Wait(cmd)
	var token string
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "KOMODO_SANDBOX_CMD=") {
			token = a
		}
	}
	if token == "" {
		t.Fatalf("the command carries no marker: %v", cmd.Args)
	}
	name := "komodo-g1-" + inst.ID[:8]
	for _, call := range calls().verb("exec") {
		if len(call) >= 5 && call[1] == name && call[2] == "sh" && call[3] == "-c" && strings.Contains(call[4], token) && strings.Contains(call[4], "kill") {
			return
		}
	}
	t.Fatalf("no kill reached the container: %v", calls().verb("exec"))
}

func TestDockerKillSkipsTheSweepWhenTheCommandEndedOnItsOwn(t *testing.T) {
	calls := fakeDocker(t)
	d := NewDocker(DockerOptions{})
	s := testSpec(t, RoleTester)
	goRepo(t, s)
	inst, _ := d.Prepare(context.Background(), s)
	cmd, _ := d.Command(context.Background(), inst, s, Command{Argv: []string{"true"}})
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(calls().verb("exec"), func(c []string) bool { return len(c) > 3 && c[2] == "sh" }) {
		t.Fatal("a command that ended must not trigger a kill")
	}
}
