package bus

import (
	"reflect"
	"sync"
)

const (
	TypeNext        Type = "next"
	TypePlan        Type = "plan"
	TypeSkill       Type = "skill"
	TypeMCP         Type = "mcp"
	TypeRunStart    Type = "run-start"
	TypeRunStop     Type = "run-stop"
	TypeRunContinue Type = "run-continue"
	TypeRunStatus   Type = "run-status"
	TypeRunComplete Type = "run-complete"
	TypeRunFailure  Type = "run-failure"
	TypeResource    Type = "resource"
	TypeError       Type = "error"
)

// allTypes lists the 12 event types.
var allTypes = []Type{
	TypeNext, TypePlan, TypeSkill, TypeMCP, TypeRunStart, TypeRunStop, TypeRunContinue,
	TypeRunStatus, TypeRunComplete, TypeRunFailure, TypeResource, TypeError,
}

// entry is one registry row: the constructor of a pair's data struct and that struct's type.
type entry struct {
	newData func() Payload
	typ     reflect.Type
}

var (
	registryMu sync.RWMutex
	registry   = map[Key]entry{}
)

// Register binds a type and subtype pair to its data struct; called once per pair at start.
// A second call for the same pair replaces the first.
func Register(k Key, newData func() Payload) {
	typ := elemType(reflect.TypeOf(newData()))
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[k] = entry{newData: newData, typ: typ}
}

// lookup returns the data constructor bound to a pair.
func lookup(k Key) (func() Payload, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	en, ok := registry[k]
	return en.newData, ok
}

// registeredKeys returns every bound pair, in no set order.
func registeredKeys() []Key {
	registryMu.RLock()
	defer registryMu.RUnlock()
	keys := make([]Key, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	return keys
}

// validate reports whether e names a registered pair and carries that pair's struct, as a value or a non-nil pointer.
func validate(e Event) bool {
	if e.Data == nil {
		return false
	}
	v := reflect.ValueOf(e.Data)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return false
	}
	registryMu.RLock()
	en, ok := registry[Key{Type: e.Type, Subtype: e.Subtype}]
	registryMu.RUnlock()
	return ok && elemType(v.Type()) == en.typ
}

// elemType returns t, or the type it points to when t is a pointer.
func elemType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// The data structs this chapter defines are bound here; the other owning packages register their own.
func init() {
	for _, p := range []func() Payload{
		func() Payload { return &RunStart{} },
		func() Payload { return &RunStop{} },
		func() Payload { return &RunContinue{} },
		func() Payload { return &RunStartedFact{} },
		func() Payload { return &RunDone{} },
		func() Payload { return &RunStopped{} },
		func() Payload { return &GroupPaused{} },
		func() Payload { return &GroupBlocked{} },
		func() Payload { return &LeaseLost{} },
		func() Payload { return &StageEnter{} },
		func() Payload { return &StageExit{} },
		func() Payload { return &SessionStart{} },
		func() Payload { return &SessionIdle{} },
		func() Payload { return &SessionTimewarn{} },
		func() Payload { return &SessionEnd{} },
		func() Payload { return &TaskClaimed{} },
		func() Payload { return &TaskReleased{} },
		func() Payload { return &TaskDone{} },
		func() Payload { return &TaskRetry{} },
		func() Payload { return &FindingSubmit{} },
		func() Payload { return &FindingVerified{} },
		func() Payload { return &ToolDenied{} },
		func() Payload { return &SandboxFallback{} },
		func() Payload { return &ToolOutside{} },
		func() Payload { return &EpicPaused{} },
		func() Payload { return &EpicResumed{} },
		func() Payload { return &TimerDue{} },
		func() Payload { return &CheckDone{} },
		func() Payload { return &ProviderSkip{} },
		func() Payload { return &LockTaken{} },
		func() Payload { return &LockReleased{} },
		func() Payload { return &LeaseTaken{} },
		func() Payload { return &LimitWarn{} },
		func() Payload { return &BusOverflow{} },
		func() Payload { return &RequestAccepted{} },
		func() Payload { return &RequestRefused{} },
	} {
		Register(p().EventKey(), p)
	}
}
