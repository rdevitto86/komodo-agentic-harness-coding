// Package komodo holds the files compiled into the komodo binary.
package komodo

import "embed"

// Embedded holds the built-in settings table and the default agent registry.
//
//go:embed embed/defaults.json embed/agents
var Embedded embed.FS
