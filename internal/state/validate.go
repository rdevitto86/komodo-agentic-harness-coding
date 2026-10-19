package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// maxRequests is the most request ids a record keeps; the oldest are dropped past it.
const maxRequests = 64

var (
	// stageValues lists every stage a record may hold.
	stageValues = []Stage{
		StageIntake,
		StageBuild,
		StageTestReview,
		StageCommit,
		StageCleanup,
		StageDone,
		StagePaused,
		StageBlocked,
	}

	// reasonValues lists every block and session-exit reason.
	reasonValues = []Reason{
		ReasonNoProvider,
		ReasonPauseTimeout,
		ReasonPaidRefused,
		ReasonFlap,
		ReasonRetries,
		ReasonNeedsScope,
		ReasonContext,
		ReasonSetup,
		ReasonPublish,
		ReasonMergeConflict,
		ReasonCleanup,
		ReasonWall,
		ReasonDependency,
		ReasonCrash,
		ReasonContract,
		ReasonStateInvalid,
		ReasonTester,
		ReasonFlaky,
		ReasonDone,
		ReasonCheckFail,
		ReasonNoResult,
		ReasonSchemaFail,
		ReasonTimeout,
		ReasonIdle,
		ReasonRefusals,
		ReasonProvider,
		ReasonBilling,
		ReasonPaused,
		ReasonCancelled,
	}

	// fieldEnums lists the allowed strings of a field by "Type.Field"; the checks and the schema files share it.
	fieldEnums = map[string][]string{
		"TaskState.State":    {"queued", "claimed", "done"},
		"RoundState.Result":  {"", "pass", "fail", "cancelled"},
		"GroupState.Outcome": {"", "complete", "lost", "abandoned", "failed"},
		"GroupState.Prior":   {"", "intake", "build", "test_review", "commit", "cleanup"},
		"PauseState.Cause":   {"provider", "plan_window", "spending", "group_tokens"},
		"LinkRecord.Cap":     {"", "window", "weekly", "model", "spend", "quota"},
	}
)

// validKey reports whether a group id is safe as a folder name.
func validKey(k string) bool {
	return k != "" && k != "." && k != ".." && !strings.ContainsAny(k, `/\`) && !strings.ContainsRune(k, 0)
}

// normalizeGroup puts a decoded group state in its stored form: empty open-action arguments become {}.
func normalizeGroup(g *GroupState) {
	for i := range g.ActionsOpen {
		if a, ok := objectOrEmpty(g.ActionsOpen[i].Args); ok {
			g.ActionsOpen[i].Args = a
		}
	}
	if len(g.Requests) > maxRequests {
		g.Requests = slices.Clone(g.Requests[len(g.Requests)-maxRequests:])
	}
}

// objectOrEmpty returns the raw value compacted, "{}" for none, and false when it is not a JSON object.
func objectOrEmpty(raw json.RawMessage) (json.RawMessage, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return json.RawMessage("{}"), true
	}

	var buf bytes.Buffer
	if t[0] != '{' || json.Compact(&buf, t) != nil {
		return nil, false
	}

	return buf.Bytes(), true
}

// validateGroup runs the schema checks of a group state decoded under the record key.
func validateGroup(key string, g *GroupState) error {
	if err := checkNilFields(key, "state", g); err != nil {
		return err
	}

	f := func(field, why string) error { return invalid(key, "state."+field, why) }
	if g.Run == "" {
		return f("run", "empty")
	}
	if g.Group != key {
		return f("group", "differs from the record's group")
	}
	if !slices.Contains(stageValues, g.Stage) {
		return f("stage", fmt.Sprintf("%q is not a stage", g.Stage))
	}
	if err := validateStageFields(g, f); err != nil {
		return err
	}
	if g.Fence == 0 {
		return f("fence", "zero")
	}
	if !isHash(g.ConfigHash) {
		return f("config_hash", "not 64 lowercase hexadecimal characters")
	}
	if err := validateCounters(&g.Counters, f); err != nil {
		return err
	}
	if err := validateParts(g, f); err != nil {
		return err
	}
	if len(g.Requests) > maxRequests {
		return f("requests", "more than 64 ids")
	}

	return nil
}

// checkNilFields refuses a nil slice or map anywhere in v.
func checkNilFields(key, root string, v any) error {
	if p := walkNil(reflect.ValueOf(v), root, false); p != "" {
		return invalid(key, p, "must be [] or {}, not null")
	}
	return nil
}

// invalid builds the refusal for one field of a group state.
func invalid(key, field, why string) error {
	return &InvalidError{Group: key, Field: field, Why: why}
}

// validateStageFields checks prior, outcome, pause and block against the stage.
func validateStageFields(g *GroupState, f func(field, why string) error) error {
	paused := g.Stage == StagePaused || g.Stage == StageBlocked
	switch {
	case g.Prior != "" && !paused:
		return f("prior", "set while the group is not paused or blocked")
	case !oneOf(string(g.Prior), fieldEnums["GroupState.Prior"]):
		return f("prior", fmt.Sprintf("%q is not a working stage", g.Prior))
	case g.Stage == StageDone && g.Outcome == "":
		return f("outcome", "empty in a done group")
	case g.Stage != StageDone && g.Outcome != "":
		return f("outcome", "set before the group is done")
	case !oneOf(g.Outcome, fieldEnums["GroupState.Outcome"]):
		return f("outcome", fmt.Sprintf("%q is not an outcome", g.Outcome))
	case g.Pause.Cause != "" && !oneOf(g.Pause.Cause, fieldEnums["PauseState.Cause"]):
		return f("pause.cause", fmt.Sprintf("%q is not a pause cause", g.Pause.Cause))
	case g.Pause.Refused < 0:
		return f("pause.refused", "negative")
	case g.Block.Reason != "" && !slices.Contains(reasonValues, g.Block.Reason):
		return f("block.reason", fmt.Sprintf("%q is not a reason", g.Block.Reason))
	}

	return nil
}

// oneOf reports whether s is in the list.
func oneOf(s string, list []string) bool { return slices.Contains(list, s) }

// isHash reports whether s is 64 lowercase hexadecimal characters.
func isHash(s string) bool {
	if len(s) != 64 {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// validateCounters refuses a negative counter.
func validateCounters(c *Counters, f func(field, why string) error) error {
	for _, n := range []struct {
		name string
		v    int64
	}{
		{"retries_left", int64(c.RetriesLeft)},
		{"overrides", int64(c.Overrides)},
		{"crashes", int64(c.Crashes)},
		{"wall_used_s", c.WallUsedS},
		{"tokens_used", c.TokensUsed},
	} {
		if n.v < 0 {
			return f("counters."+n.name, "negative")
		}
	}

	for k, v := range c.Blocks {
		if v < 0 {
			return f("counters.blocks."+k, "negative")
		}
	}

	return nil
}

// validateParts checks the tasks, the test and review results, the open actions and the unfinished edits.
func validateParts(g *GroupState, f func(field, why string) error) error {
	for i, t := range g.Tasks {
		p := fmt.Sprintf("tasks[%d].", i)
		switch {
		case t.ID == "":
			return f(p+"id", "empty")
		case !oneOf(t.State, fieldEnums["TaskState.State"]):
			return f(p+"state", fmt.Sprintf("%q is not queued, claimed or done", t.State))
		case t.GateFailures < 0:
			return f(p+"gate_failures", "negative")
		}
	}

	for _, r := range []struct {
		name  string
		round RoundState
	}{{"tester", g.TestReview.Tester}, {"reviewer", g.TestReview.Reviewer}} {
		if !oneOf(r.round.Result, fieldEnums["RoundState.Result"]) {
			return f("test_review."+r.name+".result", fmt.Sprintf("%q is not pass, fail or cancelled", r.round.Result))
		}
	}

	for i, a := range g.ActionsOpen {
		p := fmt.Sprintf("actions_open[%d].", i)
		if a.ID == "" {
			return f(p+"id", "empty")
		}
		if a.Kind == "" {
			return f(p+"kind", "empty")
		}
		if _, ok := objectOrEmpty(a.Args); !ok {
			return f(p+"args", "not a JSON object")
		}
	}

	if u := g.Unfinished; u != nil && u.Task == "" {
		return f("unfinished.task", "empty")
	}

	return nil
}

// checkMonotone refuses a write that lowers the fence or a counter below the stored record, or that raises retries_left
// without a new approved override.
func checkMonotone(key string, old, now *GroupState) error {
	f := func(field string) error {
		return invalid(key, "state."+field, "lower than the stored value")
	}
	if now.Fence < old.Fence {
		return f("fence")
	}

	oc, nc := old.Counters, now.Counters
	for _, n := range []struct {
		name     string
		old, now int64
	}{
		{"overrides", int64(oc.Overrides), int64(nc.Overrides)},
		{"crashes", int64(oc.Crashes), int64(nc.Crashes)},
		{"wall_used_s", oc.WallUsedS, nc.WallUsedS},
		{"tokens_used", oc.TokensUsed, nc.TokensUsed},
	} {
		if n.now < n.old {
			return f("counters." + n.name)
		}
	}

	if nc.RetriesLeft > oc.RetriesLeft && nc.Overrides <= oc.Overrides {
		return invalid(key, "state.counters.retries_left", "higher than the stored value with no approved override")
	}

	for k, v := range oc.Blocks {
		if nv, ok := nc.Blocks[k]; !ok || nv < v {
			return f("counters.blocks." + k)
		}
	}

	return nil
}
