package state

import (
	"strings"
	"testing"
)

// A group saved blocked on the removed qa reason loads blocked on retries, with its block count moved there.
func TestMigrationMovesAQABlockToRetries(t *testing.T) {
	m, root := newMgr(t)
	p := statePath(root, testGroup)
	writeRaw(t, p, `{"v":4,"group":"`+testGroup+`","rev":2,"ts":"2026-10-20T16:01:40Z","writer":"engine/r/1",
	 "state":{"run":"r","group":"`+testGroup+`","stage":"blocked","prior":"test_review","fence":1,
	 "config_hash":"`+testHash+`","block":{"reason":"qa"},
	 "counters":{"retries_left":2,"overrides":0,"crashes":0,"blocks":{"qa":2,"retries":1},"wall_used_s":60,
	  "tokens_used":10,"tries":1,"wall_base_s":0,"wall_ext_try":0,"fault_streak":0},
	 "test_review":{"qa":{"result":"fail","session":"s1"},"reviewer":{"result":"pass"},
	  "findings":[],"not_met":[],"failed":[]}}}`)

	r, err := m.Load(testGroup)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.State.Block.Reason != ReasonRetries || r.State.Counters.Blocks["retries"] != 3 ||
		len(r.State.Counters.Blocks) != 1 {
		t.Fatalf("block after migration: %q %v", r.State.Block.Reason, r.State.Counters.Blocks)
	}
	if _, err := m.Save(testGroup, r.State, r.Header.Rev); err != nil {
		t.Fatalf("Save after migration: %v", err)
	}
}

// A group saved before the rename loads with its Quality Assurance result, block reason and block count under qa.
func TestMigrationRenamesTheTesterToQA(t *testing.T) {
	m, root := newMgr(t)
	p := statePath(root, testGroup)
	writeRaw(t, p, `{"v":3,"group":"`+testGroup+`","rev":2,"ts":"2026-10-20T16:01:40Z","writer":"engine/r/1",
	 "state":{"run":"r","group":"`+testGroup+`","stage":"blocked","prior":"test_review","fence":1,
	 "config_hash":"`+testHash+`","block":{"reason":"tester"},
	 "counters":{"retries_left":2,"overrides":0,"crashes":0,"blocks":{"tester":1},"wall_used_s":60,"tokens_used":10,
	  "tries":1,"wall_base_s":0,"wall_ext_try":0,"fault_streak":0},
	 "test_review":{"tester":{"result":"fail","session":"s1"},"reviewer":{"result":"pass"},
	  "findings":[],"not_met":[],"failed":[]}}}`)

	r, err := m.Load(testGroup)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.State.Block.Reason != ReasonRetries || r.State.Counters.Blocks["retries"] != 1 ||
		len(r.State.Counters.Blocks) != 1 {
		t.Fatalf("block after migration: %q %v", r.State.Block.Reason, r.State.Counters.Blocks)
	}
	if r.State.TestReview.QA.Result != "fail" || r.State.TestReview.QA.Session != "s1" {
		t.Fatalf("Quality Assurance result after migration: %+v", r.State.TestReview.QA)
	}

	if _, err := m.Save(testGroup, r.State, r.Header.Rev); err != nil {
		t.Fatalf("Save after migration: %v", err)
	}
	if raw := string(readBytes(t, p)); strings.Contains(raw, "tester") {
		t.Errorf("the saved record still names the tester: %s", raw)
	}
}
