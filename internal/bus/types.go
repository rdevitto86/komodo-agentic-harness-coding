package bus

import (
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// The event types; every Event carries exactly one.
const (
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

// allTypes lists the 11 event types.
var allTypes = []Type{
	TypePlan, TypeSkill, TypeMCP, TypeRunStart, TypeRunStop, TypeRunContinue,
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
// It panics on a type outside allTypes, a pair bound before, or a constructor that does not return a non-nil pointer.
func Register(k Key, newData func() Payload) {
	if !slices.Contains(allTypes, k.Type) {
		panic(fmt.Sprintf("register event %q/%q: unknown type", k.Type, k.Subtype))
	}
	v := reflect.ValueOf(newData())
	if v.Kind() != reflect.Pointer || v.IsNil() {
		panic(fmt.Sprintf("register event %q/%q: constructor must return a non-nil pointer", k.Type, k.Subtype))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[k]; dup {
		panic(fmt.Sprintf("register event %q/%q: pair already registered", k.Type, k.Subtype))
	}
	registry[k] = entry{newData: newData, typ: v.Type().Elem()}
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

// validate reports whether e names a registered pair and carries a non-nil pointer to that pair's struct.
func validate(e Event) bool {
	v := reflect.ValueOf(e.Data)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return false
	}
	registryMu.RLock()
	en, ok := registry[Key{Type: e.Type, Subtype: e.Subtype}]
	registryMu.RUnlock()
	return ok && v.Type().Elem() == en.typ
}

// The data structs declared in data.go are bound here; other packages bind theirs with Register.
func init() {
	for _, p := range []func() Payload{
		func() Payload { return &RunStart{} },
		func() Payload { return &RunStop{} },
		func() Payload { return &RunContinue{} },
		func() Payload { return &RunStartedFact{} },
		func() Payload { return &RunDone{} },
		func() Payload { return &RunStopped{} },
		func() Payload { return &GroupPaused{} },
		func() Payload { return &GroupResumed{} },
		func() Payload { return &GroupDone{} },
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
		func() Payload { return &BusStoreFailed{} },
		func() Payload { return &RequestAccepted{} },
		func() Payload { return &RequestRefused{} },
	} {
		Register(p().EventKey(), p)
	}
}
