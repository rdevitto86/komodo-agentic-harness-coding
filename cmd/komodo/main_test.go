package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigGetListEndToEnd(t *testing.T) {
	code, out, errOut := runMain(t, "config", "get", "--list", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Items []struct{ Key string } `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK || len(env.Data.Items) < 10 {
		t.Fatalf("envelope = %s (%v)", out, err)
	}
}

func TestVersionEndToEnd(t *testing.T) {
	code, out, _ := runMain(t, "--version")
	if code != 0 || !strings.Contains(out, "1.0.0") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	code, out, _ := runMain(t, "nope", "--json")
	if code != 2 || !strings.Contains(out, `"code": "usage"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// runMain runs the binary's entry point with a temporary home folder.
func runMain(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("KOMODO_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	code := run(append([]string{"komodo"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}
