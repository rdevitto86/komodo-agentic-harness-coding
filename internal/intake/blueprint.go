// Package intake turns a request to plan into a plan folder: it runs the planner session, lints its plan.md draft and writes
// plan.md and blueprint.json under the Komodo home.
package intake

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Blueprint is the parsed plan.md, cached as blueprint.json in the plan folder.
type Blueprint struct {
	Plan    string   `json:"plan"`    // the plan id
	Repo    string   `json:"repo"`    // the repository id the plan belongs to
	Goal    string   `json:"goal"`    // one-line goal of the plan
	Release string   `json:"release"` // the release the plan delivers, such as V1
	Sources []string `json:"sources"` // repository paths of the source documents
	Groups  []Group  `json:"groups"`  // in plan order
}

// Group is one ## tg-N section of plan.md.
type Group struct {
	ID           string   `json:"id"`           // identifier from id:
	Name         string   `json:"name"`         // label inside the plan, such as tg-2
	Goal         string   `json:"goal"`         // one-line goal
	Points       int      `json:"points"`       // story points
	Subsystem    string   `json:"subsystem"`    // component name, or none
	Touches      []string `json:"touches"`      // globs the group expects to change
	Priority     Priority `json:"priority"`     // high or normal
	Files        []string `json:"files"`        // files the builder may write
	Dependencies []string `json:"dependencies"` // labels of groups that must finish first
	Tasks        []Task   `json:"tasks"`        // at most 8
	Hash         string   `json:"hash"`         // card hash of the group's section
}

// Task is one ### tsk-* section of plan.md.
type Task struct {
	Name          string       `json:"name"`           // heading text, such as tsk-fence-stale
	Card          string       `json:"card"`           // the task's write-up
	Files         []string     `json:"files"`          // files the task names, at most 5
	DoneWhen      string       `json:"done_when"`      // command that proves the task done; may be empty
	Acceptance    []Acceptance `json:"acceptance"`     // acceptance criteria; may be empty
	EstimateLines int          `json:"estimate_lines"` // planner's estimate of changed lines, at most 300
}

// Priority is a group's ordering hint.
type Priority string

// The two priorities a group may name.
const (
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
)

// Acceptance is one acceptance item.
type Acceptance struct {
	Keyword string   `json:"keyword"` // WHEN, IF or WHILE
	Text    string   `json:"text"`    // the item after the keyword, through its SHALL clause
	Cmd     string   `json:"cmd"`     // optional command Quality Assurance runs
	Needs   []string `json:"needs"`   // optional: network, docker or service:<name>
}

// ErrPlanIDTaken means every plan id drawn already exists in the repository.
var ErrPlanIDTaken = errors.New("every plan id drawn already exists")

// planIDDraws is how many ids NewPlanID draws before it gives up.
const planIDDraws = 32

// PlanDir returns the plan folder of a plan id: <home>/plans/<repo>/<plan>.
func PlanDir(home, repo, plan string) string { return filepath.Join(home, "plans", repo, plan) }

// LoadBlueprint reads blueprint.json from the plan folder of a plan id.
func LoadBlueprint(home, repo, plan string) (Blueprint, error) {
	if err := checkPlanName(repo, plan); err != nil {
		return Blueprint{}, err
	}

	data, err := os.ReadFile(filepath.Join(PlanDir(home, repo, plan), "blueprint.json"))
	if err != nil {
		return Blueprint{}, fmt.Errorf("read the blueprint of %s: %w", plan, err)
	}

	var b Blueprint
	if err := json.Unmarshal(data, &b); err != nil {
		return Blueprint{}, fmt.Errorf("decode the blueprint of %s: %w", plan, err)
	}

	return b, nil
}

// WriteBlueprint writes blueprint.json into the plan folder of the blueprint, replacing any earlier file whole.
func WriteBlueprint(home string, b Blueprint) error {
	if err := checkPlanName(b.Repo, b.Plan); err != nil {
		return err
	}

	dir := PlanDir(home, b.Repo, b.Plan)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the plan folder %s: %w", dir, err)
	}

	return writeBlueprintIn(dir, b)
}

// writeBlueprintIn writes blueprint.json into dir through a temporary file and one rename.
func writeBlueprintIn(dir string, b Blueprint) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the blueprint of %s: %w", b.Plan, err)
	}

	tmp, err := os.CreateTemp(dir, "blueprint-*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary blueprint in %s: %w", dir, err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the blueprint of %s: %w", b.Plan, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the blueprint of %s: %w", b.Plan, err)
	}

	if err := os.Rename(tmp.Name(), filepath.Join(dir, "blueprint.json")); err != nil {
		return fmt.Errorf("replace the blueprint of %s: %w", b.Plan, err)
	}

	return nil
}

// NewPlanID returns plan- and 6 random lowercase hexadecimal characters, drawn again while exists reports the id taken.
func NewPlanID(exists func(id string) bool) (string, error) { return newPlanID(exists, rand.Read) }

// newPlanID draws ids from read until one is free, up to planIDDraws times.
func newPlanID(exists func(id string) bool, read func([]byte) (int, error)) (string, error) {
	for range planIDDraws {
		var raw [3]byte
		if _, err := read(raw[:]); err != nil {
			return "", fmt.Errorf("draw a plan id: %w", err)
		}

		id := "plan-" + hex.EncodeToString(raw[:])
		if exists == nil || !exists(id) {
			return id, nil
		}
	}

	return "", ErrPlanIDTaken
}

// checkPlanName refuses an empty repository or plan id, and one that would leave the plans folder.
func checkPlanName(repo, plan string) error {
	for _, n := range []string{repo, plan} {
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\`) {
			return fmt.Errorf("repository id %q or plan id %q is empty or holds a path separator", repo, plan)
		}
	}

	return nil
}
