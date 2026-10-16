package intake

// Graph is the dependency graph of the task lists: groups are nodes, "must finish first" are edges.
type Graph struct {
	Groups  []string            // group names in folder-name order
	Deps    map[string][]string // group name -> groups that must finish first
	Touches map[string][]string // group name -> its touches globs
}
