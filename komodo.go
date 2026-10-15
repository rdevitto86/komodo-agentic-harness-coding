// Package komodo holds the files compiled into the komodo binary.
package komodo

import "embed"

// Embedded holds the built-in settings table, the default agent registry, the output templates, the state record schemas, the skills, the agent prompt templates and the repo map language patterns.
//
//go:embed embed/defaults.json embed/agents embed/output embed/state.schema.json embed/machine.schema.json embed/skills embed/templates embed/langs
var Embedded embed.FS
