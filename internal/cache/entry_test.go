package cache

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEntryFileIsJSONWithVersionHashAndPayload(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	key := c.Key("x")
	if err := c.Put(NSContext, key, []byte(`{"rank": 1}`)); err != nil {
		t.Fatal(err)
	}
	path, err := c.entryPath(NSContext, key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/context/"+string(key[0:2])+"/"+string(key[2:4])+"/"+string(key)+".json") {
		t.Fatalf("path = %s; want <ns>/<key[0:2]>/<key[2:4]>/<key>.json", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("entry file is not JSON: %v", err)
	}
	if f["v"] != float64(1) || len(f["sha256"].(string)) != 64 || f["payload"] != "eyJyYW5rIjogMX0=" {
		t.Fatalf("entry = %v; want v 1, a 64-character sha256 and the base64 payload", f)
	}
}

func TestDecodeEntryRejectsAChangedPayloadByte(t *testing.T) {
	raw := encodeEntry([]byte("payload bytes"))
	if got, ok := decodeEntry(raw); !ok || !bytes.Equal(got, []byte("payload bytes")) {
		t.Fatalf("decode = %q, %v; want the payload", got, ok)
	}
	var f entryFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	f.Payload[0] ^= 1
	changed, _ := json.Marshal(f)
	if _, ok := decodeEntry(changed); ok {
		t.Fatal("a payload changed by one byte must fail the check")
	}
	f.Payload[0] ^= 1
	f.V = 2
	other, _ := json.Marshal(f)
	if _, ok := decodeEntry(other); ok {
		t.Fatal("an unknown schema version must fail the check")
	}
	if _, ok := decodeEntry([]byte("not json")); ok {
		t.Fatal("a file that is not JSON must fail the check")
	}
}

func TestEmptyPayloadRoundTrips(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	key := c.Key("empty")
	if err := c.Put(NSSkills, key, nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get(NSSkills, key); !ok || len(got) != 0 {
		t.Fatalf("Get = %q, %v; want an empty hit", got, ok)
	}
}

func TestValidKeyAndNamespace(t *testing.T) {
	good := strings.Repeat("ab", 32)
	for key, want := range map[string]bool{good: true, good[:63]: false, strings.ToUpper(good): false, strings.Repeat("g", 64): false, "": false} {
		if validKey(Key(key)) != want {
			t.Errorf("validKey(%q) = %v; want %v", key, !want, want)
		}
	}
	for _, ns := range []Namespace{NSSkills, NSContext, NSMap, NSSetup, NSPrompt} {
		if !validNamespace(ns) {
			t.Errorf("namespace %q must be valid", ns)
		}
	}
	if validNamespace("checks") || validNamespace("review") || validNamespace("../x") {
		t.Error("unknown namespaces must be refused")
	}
}
