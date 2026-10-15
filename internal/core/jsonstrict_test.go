package core

import "testing"

func TestDecodeStrict(t *testing.T) {
	type rec struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	var r rec
	if err := DecodeStrict([]byte(`{"a": 1, "b": "x"}`), &r); err != nil || r.A != 1 || r.B != "x" {
		t.Fatalf("good record: %+v %v", r, err)
	}
	for _, bad := range []string{`{"a": 1, "c": 2}`, `{"a": 1} {}`, `{"a": 1} x`, `nope`, ``, `{"a": "s"}`} {
		if err := DecodeStrict([]byte(bad), &rec{}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
