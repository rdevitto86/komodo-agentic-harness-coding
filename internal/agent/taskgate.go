package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	GateFeedbacksPerTask = 2    // gate results sent back to a live session before check_fail
	GateOutputCap        = 4096 // bytes of command output a gate result keeps, head and tail
)

// truncMarker replaces the middle of an output that was cut.
const truncMarker = "\n... output cut ...\n"

// CommandRunner runs one command in the sandbox and returns its exit code and capped output.
type CommandRunner func(ctx context.Context, command string) (exit int, output string, err error)

// TaskRef names the task being finished.
type TaskRef struct {
	Group       string
	Task        string
	DoneCommand string // the task's completion command
}

// TaskOutcome is the result of one gate run.
type TaskOutcome struct {
	ExitCode int
	Output   string // combined output, capped at 4 KB with head and tail kept
}

// TaskGate finishes a task: it reruns the completion command and returns its outcome.
type TaskGate struct {
	Run CommandRunner
}

// Complete reruns the task's completion command; it writes nothing to git.
func (g TaskGate) Complete(ctx context.Context, t TaskRef) (TaskOutcome, error) {
	if g.Run == nil {
		return TaskOutcome{}, errors.New("task gate has no command runner")
	}
	if strings.TrimSpace(t.DoneCommand) == "" {
		return TaskOutcome{}, fmt.Errorf("task %s has no completion command", t.Task)
	}
	exit, out, err := g.Run(ctx, t.DoneCommand)
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("run completion command of %s: %w", t.Task, err)
	}
	return TaskOutcome{ExitCode: exit, Output: CapOutput(out, GateOutputCap)}, nil
}

// CapOutput keeps the head and the tail of s within max bytes, cutting only at character boundaries.
func CapOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	keep := max - len(truncMarker)
	if keep < 2 {
		end := max
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		return s[:end]
	}
	head, tail := keep/2, keep-keep/2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	start := len(s) - tail
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[:head] + truncMarker + s[start:]
}
