package metrics

import (
	"path/filepath"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// failuresFile is the machine metrics file of the Service's own failures that belong to no run's figures.
const failuresFile = "failures.jsonl"

// eventsDirOf returns the events folder that sits beside a repository's metrics folder, as the bus lays it out.
func eventsDirOf(dir string) string { return bus.EventsDir(filepath.Dir(filepath.Dir(dir))) }

// RepoDir returns a repository's metrics folder inside its git common directory.
func RepoDir(commonDir string) string { return filepath.Join(commonDir, "komodo", "metrics") }
