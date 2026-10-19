package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// maxHookInput is the largest PreToolUse payload the hook guard reads; a Write call carries the whole file.
const maxHookInput = 64 << 20

// HookInput is the part of Claude Code's PreToolUse payload the hook guard reads.
type HookInput struct {
	Tool  string          `json:"tool_name"`  // Bash, Edit or Write for the tools the hook is installed for
	Input json.RawMessage `json:"tool_input"` // the arguments Claude Code passes to that tool
	Cwd   string          `json:"cwd"`        // the folder the Claude Code session runs in
}

// ReadHookInput reads one PreToolUse payload from r; an empty, oversized or malformed payload is an error.
func ReadHookInput(r io.Reader) (HookInput, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxHookInput+1))
	if err != nil {
		return HookInput{}, fmt.Errorf("read the hook input: %w", err)
	}
	if len(raw) > maxHookInput {
		return HookInput{}, errors.New("the hook input is larger than 64 MB")
	}

	var in HookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return HookInput{}, fmt.Errorf("parse the hook input: %w", err)
	}
	if in.Tool == "" {
		return HookInput{}, errors.New("the hook input names no tool")
	}

	return in, nil
}

// CheckHook decides one Claude Code tool call made in the repository at root. Edit and Write run through the rows of the
// edit and write tools; Bash through the text rows; any other tool is allowed. An unreadable call is an error.
func CheckHook(home, root string, in HookInput) (Verdict, error) {
	if in.Tool != "Edit" && in.Tool != "Write" && in.Tool != "Bash" {
		return Verdict{Allowed: true}, nil
	}

	rows, err := LoadRows(home)
	if err != nil {
		return Verdict{}, fmt.Errorf("load the guard rows: %w", err)
	}

	bind := Binding{
		Agent:       "orchestrator",
		Permissions: Permissions{Tools: []string{"edit", "write"}},
		Worktree:    root,
	}

	// Each hook call is a fresh process, so no refusal count can reach a limit.
	g, err := NewGuard(rows, bind, math.MaxInt)
	if err != nil {
		return Verdict{}, fmt.Errorf("compile the guard: %w", err)
	}

	switch in.Tool {
	case "Edit":
		var a struct {
			Path string `json:"file_path"`
			Old  string `json:"old_string"`
			New  string `json:"new_string"`
		}
		if err := json.Unmarshal(in.Input, &a); err != nil {
			return Verdict{}, fmt.Errorf("read the Edit input: %w", err)
		}

		args, err := json.Marshal(editArgs{Path: a.Path, Old: a.Old, New: a.New})
		if err != nil {
			return Verdict{}, fmt.Errorf("encode the edit call: %w", err)
		}

		return g.Check(Call{Tool: "edit", Args: args}), nil
	case "Write":
		var a struct {
			Path    string `json:"file_path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(in.Input, &a); err != nil {
			return Verdict{}, fmt.Errorf("read the Write input: %w", err)
		}

		args, err := json.Marshal(writeArgs{Path: a.Path, Content: a.Content})
		if err != nil {
			return Verdict{}, fmt.Errorf("encode the write call: %w", err)
		}

		return g.Check(Call{Tool: "write", Args: args}), nil
	}

	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(in.Input, &a); err != nil {
		return Verdict{}, fmt.Errorf("read the Bash input: %w", err)
	}

	return g.CheckText("Bash", a.Command), nil
}
