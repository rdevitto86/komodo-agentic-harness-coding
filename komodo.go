// Package komodo holds the files compiled into the komodo binary.
package komodo

import "embed"

// Embedded holds the built-in settings table, the default agent registry and the output templates.
//
//go:embed embed/defaults.json embed/agents embed/output
var Embedded embed.FS
