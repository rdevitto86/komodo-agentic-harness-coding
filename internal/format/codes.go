package format

import "slices"

// Code is one word from the closed error code list.
type Code string

const (
	ConfigTampered Code = "config_tampered"
	Pinned         Code = "pinned"
	NoTerminal     Code = "no_terminal"
	NotBlocked     Code = "not_blocked"
	GroupFailed    Code = "group_failed"
	NoRepo         Code = "no_repo"
	Usage          Code = "usage"
	UnknownKey     Code = "unknown_key"
	BadValue       Code = "bad_value"
	OverLimit      Code = "over_limit"
	UnknownTarget  Code = "unknown_target"
	RunRefused     Code = "run_refused"
	EngineStopping Code = "engine_stopping"
	BadFrame       Code = "bad_frame"
	CardChanged    Code = "card_changed"
	Locked         Code = "locked"
	BusOverflow    Code = "bus_overflow"
	StoreFailed    Code = "store_failed"
	PlanBlocked    Code = "plan_blocked"
	PlanTimeout    Code = "plan_timeout"
	PlanInput      Code = "plan_input"
	PlanWrite      Code = "plan_write"
	ContextRefused Code = "context_refused"
	Internal       Code = "internal"
	Other          Code = "other"
)

// allCodes lists every code on the closed list, in list order.
var allCodes = []Code{
	ConfigTampered, Pinned, NoTerminal, NotBlocked, GroupFailed, NoRepo, Usage, UnknownKey, BadValue, OverLimit,
	UnknownTarget, RunRefused, EngineStopping, BadFrame, CardChanged, Locked, BusOverflow,
	StoreFailed, PlanBlocked, PlanTimeout, PlanInput, PlanWrite, ContextRefused, Internal, Other,
}

// blockReasons lists the reasons a group is blocked; each is the code of a needs.<reason> shape.
var blockReasons = []string{
	"no_provider",
	"pause_timeout",
	"paid_refused",
	"flap",
	"retries",
	"needs_scope",
	"context",
	"setup",
	"publish",
	"merge_conflict",
	"cleanup",
	"wall",
	"budget",
	"dependency",
	"crash",
	"contract",
	"state_invalid",
	"flaky",
}

// AllCodes returns a copy of the closed code list.
func AllCodes() []Code { return slices.Clone(allCodes) }

// IsCode reports whether s is on the closed error code list.
func IsCode(s string) bool { return slices.Contains(allCodes, Code(s)) }

// BlockReasons returns a copy of the closed list of block reasons.
func BlockReasons() []string { return slices.Clone(blockReasons) }

// IsBlockReason reports whether s is on the closed list of block reasons.
func IsBlockReason(s string) bool { return slices.Contains(blockReasons, s) }
