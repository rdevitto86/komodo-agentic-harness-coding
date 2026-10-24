package fault

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// TestKindExitCode checks the exit code each kind selects.
func TestKindExitCode(t *testing.T) {
	cases := map[Kind]int{KindRefused: 1, KindUsage: 2, KindBlocked: 3, KindInternal: 4, Kind("bogus"): 4}
	for k, want := range cases {
		if got := k.ExitCode(); got != want {
			t.Errorf("%q: exit %d, want %d", k, got, want)
		}
	}
}

// TestEventKeys checks each fault struct binds the type and subtype.
func TestEventKeys(t *testing.T) {
	cases := map[string]bus.Payload{
		"fault.error":    ErrorFault{},
		"fault.panic":    PanicFault{},
		"fault.resource": ResourceFault{},
	}
	for st, p := range cases {
		if k := p.EventKey(); k.Type != bus.TypeError || string(k.Subtype) != st {
			t.Errorf("%T: key %+v, want error/%s", p, k, st)
		}
	}
}

// TestDecodeContractLines decodes the two event lines through the bus registry.
func TestDecodeContractLines(t *testing.T) {
	panicLine := `{"type":"error","subtype":"fault.panic","run":"0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57",` +
		`"group":"0192e4c5-7a10-7d3e-9b2f-6c1d8e4a5f30","time":"2026-10-14T16:02:45Z","data":{"component":"task",` +
		`"code":"internal","kind":"internal","severity":"error","reason":"index out of range [3] with length 3",` +
		`"panic":true,"stack":"goroutine 41 [running]:\n..."}}`
	resLine := `{"type":"error","subtype":"fault.resource","run":"0192e4c5-6f00-7a21-8c3b-2d9e0f1a4b57","group":"",` +
		`"time":"2026-10-14T16:10:00Z","data":{"component":"resource","code":"over_limit","kind":"refused",` +
		`"severity":"warn","reason":"plan window 82% used on claude-cli","fix":"wait for the window reset"}}`
	for _, line := range []string{panicLine, resLine} {
		var e bus.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode: %v", err)
		}
		back, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var a, b map[string]any
		_ = json.Unmarshal([]byte(line), &a)
		_ = json.Unmarshal(back, &b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("round trip changed the line:\n got %s\nwant %s", jb, ja)
		}
	}
}

// TestFaultIsError checks a Fault used as an error reports its reason.
func TestFaultIsError(t *testing.T) {
	var err error = Fault{Reason: "boom"}
	if err.Error() != "boom" {
		t.Fatalf("got %q", err.Error())
	}
}

// TestFaultDataCapsStack checks a stack is cut to 4 KB and a short one is kept.
func TestFaultDataCapsStack(t *testing.T) {
	d := toData(Fault{Panic: true, Stack: strings.Repeat("x", 10_000)})
	if len(d.Stack) != 4096 {
		t.Fatalf("stack %d bytes, want 4096", len(d.Stack))
	}
	if d := toData(Fault{Stack: "s"}); d.Stack != "s" {
		t.Fatalf("short stack changed: %q", d.Stack)
	}
}

// TestFaultDataCapKeepsRunes checks the cut never splits a multi-byte character.
func TestFaultDataCapKeepsRunes(t *testing.T) {
	d := toData(Fault{Stack: strings.Repeat("é", 3000)})
	if len(d.Stack) > 4096 || !strings.HasSuffix(d.Stack, "é") {
		t.Fatalf("bad cut: %d bytes", len(d.Stack))
	}
}

// TestPanicReason checks the runtime prefix is dropped from a runtime panic's reason.
func TestPanicReason(t *testing.T) {
	var s []int
	var got string
	func() {
		defer func() { got = panicReason(recover()) }()
		_ = s[3]
	}()
	if got != "index out of range [3] with length 0" {
		t.Fatalf("got %q", got)
	}
	if panicReason("custom") != "custom" {
		t.Fatal("plain value changed")
	}
}

// TestCodesAreOnTheClosedList checks every code the package emits is on the Output Formatter's list.
func TestCodesAreOnTheClosedList(t *testing.T) {
	codes := []string{string(format.Internal), string(format.OverLimit), string(format.BusOverflow)}
	for _, m := range otherErrors {
		codes = append(codes, string(m.code))
	}
	for _, c := range codes {
		if !format.IsCode(c) {
			t.Errorf("%q is not on the closed code list", c)
		}
	}
}

// TestOtherErrorTable checks each non-fault error subtype maps to the code, kind and severity.
func TestOtherErrorTable(t *testing.T) {
	want := map[bus.Subtype][4]string{
		"plan-blocked":    {"plan_blocked", "blocked", "error", "intake"},
		"plan-timeout":    {"plan_timeout", "refused", "error", "intake"},
		"plan-input":      {"plan_input", "usage", "error", "intake"},
		"plan-locked":     {"locked", "refused", "error", "intake"},
		"plan-write":      {"plan_write", "internal", "error", "intake"},
		"context-refused": {"context_refused", "refused", "error", "contextmgr"},
		"run-refused":     {"run_refused", "refused", "error", "engine"},
	}
	for st, w := range want {
		m, ok := otherErrors[st]
		if !ok {
			t.Errorf("%s missing", st)
			continue
		}
		if got := [4]string{string(m.code), string(m.kind), string(m.severity), m.component}; got != w {
			t.Errorf("%s: %v, want %v", st, got, w)
		}
	}
	if len(otherErrors) != len(want) {
		t.Errorf("%d rows, want %d", len(otherErrors), len(want))
	}
}

// Each fault data type reports its final flag through the bus's Finaler interface.
func TestFaultDataIsAFinaler(t *testing.T) {
	for _, p := range []bus.Payload{&ErrorFault{}, &PanicFault{}, &ResourceFault{}} {
		f, ok := p.(bus.Finaler)
		if !ok || f.IsFinal() {
			t.Fatalf("%T: Finaler %v, final %v, want a Finaler that is not final", p, ok, ok && f.IsFinal())
		}
	}
	d := &ErrorFault{FaultData{Final: true}}
	if !d.IsFinal() {
		t.Fatal("a final fault reports false")
	}
}
