// Package komodo holds the files compiled into the komodo binary.
package komodo

import "embed"

// Embedded holds the built-in settings table, the default agent registry, the output templates and the state record schemas.
//
//go:embed embed/defaults.json embed/agents embed/output embed/state.schema.json embed/machine.schema.json
var Embedded embed.FS
