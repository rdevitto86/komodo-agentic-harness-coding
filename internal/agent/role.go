// Package agent holds the role definitions, result schemas and pure decision functions of the Builder, Quality Assurance,
// Reviewer and Planner.
package agent

import (
	"encoding/json"
	"reflect"
	"slices"
)

// Slots lists the brief slots a role must receive and those it may receive.
type Slots struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

// Role is the compiled definition of one agent role.
type Role struct {
	Name       string          `json:"name"`        // builder, Quality Assurance, reviewer or planner
	Tools      []string        `json:"tools"`       // the only tools advertised to the session
	RunClasses []string        `json:"run_classes"` // the only classes the run tool may execute
	Guard      []string        `json:"guard"`       // guard rows beyond the inherited G1 to G6
	Slots      Slots           `json:"slots"`
	Result     json.RawMessage `json:"result"`    // JSON Schema of the final call
	PoolSlot   bool            `json:"pool_slot"` // takes one of the machine-wide session slots
}

// Roles returns the four compiled roles in registry order: planner, builder, Quality Assurance, reviewer.
func Roles() []Role {
	return []Role{PlannerRole(), BuilderRole(), QARole(), ReviewerRole()}
}

// RoleByName returns the compiled role with the given name.
func RoleByName(name string) (Role, bool) {
	for _, r := range Roles() {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// newRole builds a role whose slices are fresh copies, so a caller can never change the compiled definition.
func newRole(
	name string,
	tools, classes, guard, required, optional []string,
	resultID string,
	result any,
	pool bool,
) Role {
	schema := resultSchema(resultID, reflect.TypeOf(result))
	return Role{
		Name:       name,
		Tools:      slices.Clone(tools),
		RunClasses: slices.Clone(classes),
		Guard:      slices.Clone(guard),
		Slots:      Slots{Required: slices.Clone(required), Optional: slices.Clone(optional)},
		Result:     schema,
		PoolSlot:   pool,
	}
}
