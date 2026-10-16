package commit

import "github.com/rdevitto86/komodo-agentic-harness-coding/internal/core"

// CheckMessage refuses an empty message and one whose last paragraph is only Name: value lines.
func CheckMessage(msg string) error { return core.CheckMessage(msg) }
