package sandbox

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests pin what each mechanism does today for an outside tool server that lists hosts. The Spec has no host list,
// so such a server gets the network-off instance; no mechanism here limits a server to hosts.

// The Spec carries one network switch and no host list.
func TestSpecHasOnlyANetworkSwitch(t *testing.T) {
	typ := reflect.TypeOf(Spec{})
	if f, ok := typ.FieldByName("Network"); !ok || f.Type.Kind() != reflect.Bool {
		t.Fatalf("Network field = %+v", f)
	}
	for i := 0; i < typ.NumField(); i++ {
		if n := strings.ToLower(typ.Field(i).Name); strings.Contains(n, "host") || strings.Contains(n, "allow") {
			t.Errorf("Spec field %s lists hosts; update the outside-server design and this test", typ.Field(i).Name)
		}
	}
}

// With the network off, Seatbelt allows loopback only and states no per-host outbound rule.
func TestSeatbeltProfileWithoutNetworkAllowsLoopbackOnly(t *testing.T) {
	text := seatbeltProfile(mustRules(t, fakeSpec()), "/Users/dev")
	if !strings.Contains(text, `(allow network-outbound (remote ip "localhost:*"))`) {
		t.Error("loopback outbound rule is missing")
	}
	if strings.Contains(text, "(allow network-outbound (remote ip))") {
		t.Error("the profile allows every remote address")
	}
}

// With the network off, Bubblewrap unshares the network namespace, which leaves only a loopback device.
func TestBubblewrapWithoutNetworkUnsharesTheNamespace(t *testing.T) {
	if a := bwrapArgs(mustRules(t, fakeSpec()), "/home/dev", false); !slices.Contains(a, "--unshare-net") {
		t.Errorf("args = %v", a)
	}
}

// With the network off, Docker creates the container on the none network, which has only a loopback device.
func TestDockerWithoutNetworkUsesTheNoneNetwork(t *testing.T) {
	args, _ := NewDocker(DockerOptions{}).createArgs(mustRules(t, fakeSpec()), Instance{ID: "i", Group: "g1"}, "n", "img")

	i := slices.Index(args, "--network")
	if i < 0 || args[i+1] != "none" {
		t.Errorf("args = %v", args)
	}
}

// Seatbelt takes a host only as * or localhost, so a listed name or address cannot be written into a profile.
// This runs the real sandbox-exec on macOS; it only parses a profile and starts /usr/bin/true.
func TestSeatbeltCannotNameAHost(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt runs on macOS only")
	}
	bin, err := exec.LookPath("sandbox-exec")
	if err != nil {
		t.Skip("sandbox-exec is not installed")
	}

	for _, tc := range []struct {
		name, filter string
		ok           bool
	}{
		{"a host name", `(remote ip "example.com:443")`, false},
		{"an address", `(remote ip "192.0.2.1:443")`, false},
		{"an address range", `(remote ip "10.0.0.0/8:443")`, false},
		{"any host, one port", `(remote tcp "*:443")`, true},
		{"loopback", `(remote ip "localhost:*")`, true},
	} {
		profile := "(version 1)(allow default)(deny network*)(allow network-outbound " + tc.filter + ")"

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, bin, "-p", profile, "/usr/bin/true").CombinedOutput()
		cancel()

		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v output=%q", tc.name, err, out)
		}
		if !tc.ok && !strings.Contains(string(out), "host must be * or localhost") {
			t.Errorf("%s: unexpected refusal %q", tc.name, out)
		}
	}
}

// A real Seatbelt instance with the network off reaches loopback and nothing else: a listed host would get no network.
// Opt in with KOMODO_TEST_SANDBOX_EXEC=1, like the other live Seatbelt test.
func TestSeatbeltInstanceWithoutNetworkReachesLoopbackOnly(t *testing.T) {
	if os.Getenv("KOMODO_TEST_SANDBOX_EXEC") == "" {
		t.Skip("set KOMODO_TEST_SANDBOX_EXEC=1 to run real sandbox-exec commands")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is not installed")
	}

	s := testSpec(t, RoleBuilder)
	m := NewManager(map[string]Backend{RoleBuilder: NewSeatbelt(SeatbeltOptions{Home: realPath(t, t.TempDir())})}, nil)

	inst, err := m.CreateSandboxInstance(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DestroySandboxInstance(context.Background(), inst.ID) })

	loop := listenPort(t, "127.0.0.1")
	for _, tc := range []struct {
		name, script string
		ok           bool
	}{
		{"loopback", "/usr/bin/nc -z -w 2 127.0.0.1 " + loop, true},
		{"a listed public address", "/usr/bin/nc -z -w 2 192.0.2.1 443", false},
		{"a listed name", "/usr/bin/nc -z -w 2 example.com 443", false},
	} {
		cmd, err := m.Command(context.Background(), inst.ID, Command{
			Argv:    []string{"/bin/sh", "-c", tc.script},
			Timeout: 20 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v output=%q", tc.name, err, out)
		}
	}
}
