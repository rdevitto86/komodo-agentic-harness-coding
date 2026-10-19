package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dataItems decodes the items of a config get envelope.
func dataItems(t *testing.T, e Envelope) []ConfigItemData {
	t.Helper()
	b, _ := json.Marshal(e.Data)
	var d ConfigGetData
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d.Items
}

func TestConfigGetKey(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("config", "get", "limits.group_retries")
	items := dataItems(t, e)
	if code != 0 || !e.OK || len(items) != 1 || items[0].Key != "limits.group_retries" || items[0].Source != "built-in" {
		t.Fatalf("exit %d, %+v", code, e)
	}
	if v, _ := items[0].Value.(float64); v != 3 {
		t.Fatalf("value = %#v", items[0].Value)
	}
	if !strings.Contains(e.Report, "limits.group_retries") && !strings.Contains(e.Report, "1 setting") {
		t.Fatalf("report = %q", e.Report)
	}
}

func TestConfigGetListAndAll(t *testing.T) {
	h := newHarness(t)
	want := len(h.cfg.ListConfigItems(""))
	code, e := h.envelope("config", "get", "--list")
	if code != 0 || len(dataItems(t, e)) != want || want < 10 {
		t.Fatalf("--list: exit %d, %d items, want %d", code, len(dataItems(t, e)), want)
	}
	code, e = h.envelope("config", "get", "--all")
	items := dataItems(t, e)
	if code != 0 || len(items) != want {
		t.Fatalf("--all: exit %d, %d items", code, len(items))
	}
	for _, it := range items {
		if it.Key == "limits.group_retries" && (it.Limit == nil || it.Default == nil) {
			t.Fatalf("--all lacks limit or default: %+v", it)
		}
	}
	_, e = h.envelope("config", "get", "output.profile", "--all")
	if a := dataItems(t, e)[0].Allowed; len(a) != 2 || a[0] != "adhd" {
		t.Fatalf("valid values = %v", a)
	}
}

func TestConfigGetErrors(t *testing.T) {
	h := newHarness(t)
	code, e := h.envelope("config", "get", "limits.group_retrys")
	if code != 2 || e.Error.Code != "unknown_key" || !strings.Contains(e.Error.Fix, "limits.group_retries") {
		t.Fatalf("unknown key: exit %d, %+v", code, e.Error)
	}
	code, e = h.envelope("config", "get")
	if code != 2 || e.Error.Code != "usage" {
		t.Fatalf("no key: exit %d, %+v", code, e.Error)
	}
}

func TestConfigSetRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	code, e := h.envelope("config", "set", "limits.group_retries=2")
	if code != 0 || !e.OK {
		t.Fatalf("set: exit %d, %+v", code, e)
	}
	b, _ := json.Marshal(e.Data)
	var set ConfigSetData
	_ = json.Unmarshal(b, &set)
	if set.Batch == "" || len(set.Changes) != 1 || set.Changes[0].Key != "limits.group_retries" {
		t.Fatalf("data = %s", b)
	}
	_, e = h.envelope("config", "get", "limits.group_retries")
	if it := dataItems(t, e)[0]; it.Source != "machine" || it.Value.(float64) != 2 {
		t.Fatalf("after set: %+v", it)
	}
	_, e = h.envelope("config", "set", "limits.group_retries=2")
	b, _ = json.Marshal(e.Data)
	set = ConfigSetData{}
	_ = json.Unmarshal(b, &set)
	if set.Batch != "" || len(set.Changes) != 0 {
		t.Fatalf("repeat set changed something: %s", b)
	}
}

func TestConfigSetRefusals(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	cases := []struct {
		arg      string
		exit     int
		wantCode string
	}{
		{"limits.group_retries=999", 1, "over_limit"},
		{"limits.group_retries=abc", 2, "bad_value"},
		{"limits.group_retrys=2", 2, "unknown_key"},
		{"output.profile=loud", 2, "bad_value"},
	}
	for _, c := range cases {
		code, e := h.envelope("config", "set", c.arg)
		if code != c.exit || e.Error == nil || e.Error.Code != c.wantCode {
			t.Errorf("%s: exit %d, %+v", c.arg, code, e.Error)
		}
	}
	if code, e := h.envelope("config", "set", "limits.group_retries"); code != 2 || e.Error.Code != "usage" {
		t.Errorf("no equals sign: exit %d, %+v", code, e.Error)
	}
	_, e := h.envelope("config", "get", "limits.group_retries")
	if dataItems(t, e)[0].Source != "built-in" {
		t.Error("a refused set wrote a value")
	}
}

func TestConfigSetDefault(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	h.envelope("config", "set", "limits.group_retries=2")
	code, e := h.envelope("config", "set", "--default", "limits.group_retries")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e)
	}
	_, e = h.envelope("config", "get", "limits.group_retries")
	if dataItems(t, e)[0].Source != "built-in" {
		t.Fatalf("not reset: %+v", dataItems(t, e)[0])
	}
}

func TestConfigSetDefaultSectionWaitsForY(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	h.envelope("config", "set", "limits.group_retries=2", "output.profile=standard")
	h.stdin = "n\n"
	code, out, errOut := h.run("--json", "config", "set", "--default", "settings.json")
	if code != 0 || !strings.Contains(errOut, "limits.group_retries") || !strings.Contains(errOut, "output.profile") {
		t.Fatalf("decline: exit %d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	_, e := h.envelope("config", "get", "limits.group_retries")
	if dataItems(t, e)[0].Source != "machine" {
		t.Fatal("a declined reset changed a value")
	}
	h.stdin = "y\n"
	if code, out, _ := h.run("--json", "config", "set", "--default", "settings.json"); code != 0 {
		t.Fatalf("confirm: exit %d\n%s", code, out)
	}
	_, e = h.envelope("config", "get", "limits.group_retries")
	if dataItems(t, e)[0].Source != "built-in" {
		t.Fatal("a confirmed reset kept the value")
	}
}

func TestConfigSetDefaultDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	h.envelope("config", "set", "limits.group_retries=2")
	code, e := h.envelope("config", "set", "--default", "limits.group_retries", "--dry-run")
	if code != 0 || !e.OK {
		t.Fatalf("exit %d, %+v", code, e)
	}
	_, e = h.envelope("config", "get", "limits.group_retries")
	if dataItems(t, e)[0].Source != "machine" {
		t.Fatal("--dry-run wrote")
	}
}

func TestConfigTamperAndAccept(t *testing.T) {
	h := newHarness(t)
	h.terminal = true
	h.envelope("config", "set", "limits.group_retries=2")
	if err := os.WriteFile(filepath.Join(h.home, "settings.json"), []byte(`{"limits.group_retries": 4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := h.run("--json", "config", "get", "limits.group_retries")
	if code != 0 || !strings.Contains(errOut, "komodo config set --accept") || strings.Count(out, `"v"`) != 1 {
		t.Fatalf("get while tampered: exit %d\n%s\n%s", code, out, errOut)
	}
	code, e := h.envelope("config", "set", "limits.group_retries=1")
	if code != 1 || e.Error.Code != "config_tampered" {
		t.Fatalf("set while tampered: exit %d, %+v", code, e.Error)
	}
	if code, e := h.envelope("config", "set", "--accept"); code != 0 || !e.OK {
		t.Fatalf("accept: exit %d, %+v", code, e)
	}
	_, out, errOut = h.run("--json", "config", "get", "limits.group_retries")
	if errOut != "" {
		t.Fatalf("still warns after accept: %s", errOut)
	}
	if !strings.Contains(out, `"value": 4`) {
		t.Fatalf("accepted value lost: %s", out)
	}
}

func TestConfigHelpListsKeysBelowLevel(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("config", "get", "limits", "--help")
	if code != 0 || !strings.Contains(out, "limits.group_retries") || strings.Contains(out, "output.profile") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, out, _ = h.run("config", "--help")
	if code != 0 || !strings.Contains(out, "output.profile") {
		t.Fatalf("config --help: exit %d\n%s", code, out)
	}
}
