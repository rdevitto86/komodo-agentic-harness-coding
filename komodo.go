// Package komodo holds the files compiled into the komodo binary.
package komodo

import "embed"

// Embedded holds the built-in settings table, the default agent registry, the output templates, the
// state record schemas, the skills, the agent prompt templates and the repo map language patterns
// and the dashboard page.
//
//go:embed embed/defaults.json embed/agents embed/output
//go:embed embed/state.schema.json embed/machine.schema.json
//go:embed embed/skills embed/templates embed/langs embed/ui
var Embedded embed.FS
