package core

import (
	"os/exec"
	"slices"
)

// golangciConfigs lists the golangci-lint configuration file names, looked up at the repository root.
var golangciConfigs = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}

// GolangciConfigs returns the golangci-lint configuration file names, looked up at the repository root.
func GolangciConfigs() []string { return slices.Clone(golangciConfigs) }

// LookGolangci returns the path of the golangci-lint program on PATH, or an error when it is not installed.
func LookGolangci() (string, error) { return exec.LookPath("golangci-lint") }
