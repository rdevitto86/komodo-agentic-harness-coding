package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/intake"
)

// errPlanName means the text cannot name a plan folder.
var errPlanName = errors.New("not a plan id")

// PlanListData is the data of a --list reply for run and plan.
type PlanListData struct {
	Plans []string `json:"plans"` // the plan ids of the repository, in name order
}

// resolveRun checks the plan id and the group label of run against the plan's blueprint and sets the groups to run.
func resolveRun(c *Call) *Result {
	if c.Global.List || len(c.Args) == 0 {
		return nil
	}
	if len(c.Args) == 1 && runIDShape.MatchString(c.Args[0]) && flagAnswers(c) {
		return nil
	}

	bp, err := c.blueprint(c.Args[0])
	if err != nil {
		r := unknownPlan(c.Args[0])
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errPlanName) {
			r = internalErr(err.Error())
		}
		return &r
	}
	if len(c.Args) < 2 {
		return nil
	}

	if !slices.ContainsFunc(bp.Groups, func(g intake.PlanGroup) bool { return g.Name == c.Args[1] }) {
		r := usageErr("plan "+c.Args[0]+" has no group "+c.Args[1], "komodo run --list")
		return &r
	}

	c.Groups = []string{c.Args[1]}
	return nil
}

// flagAnswers reports whether the call answers a group with --approve, --deny or --override.
func flagAnswers(c *Call) bool {
	return flagBool(c, "approve") || flagBool(c, "deny") || flagBool(c, "override")
}

// blueprint loads the blueprint of a plan id of the repository the call acts on.
func (c *Call) blueprint(plan string) (intake.Blueprint, error) {
	if plan == "." || plan == ".." || strings.ContainsAny(plan, `/\`) {
		return intake.Blueprint{}, errPlanName
	}

	home, err := c.cli.komodoHome()
	if err != nil {
		return intake.Blueprint{}, fmt.Errorf("find the Komodo home folder: %w", err)
	}

	return intake.LoadBlueprint(home, intake.RepoFolder(c.Repo.ID), plan)
}

// unknownPlan builds the usage result for a plan id with no plan folder in this repository.
func unknownPlan(plan string) Result {
	return usageErr("no plan "+plan+" for this repository", "komodo run --list")
}

// listPlans prints the plan ids of the repository, one per line.
func listPlans(c *Call) Result {
	home, err := c.cli.komodoHome()
	if err != nil {
		return internalErr("find the Komodo home folder: " + err.Error())
	}

	dir := filepath.Dir(intake.PlanDir(home, intake.RepoFolder(c.Repo.ID), "x"))
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return internalErr("read the plans folder " + dir + ": " + err.Error())
	}

	ids := []string{}
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			ids = append(ids, e.Name())
		}
	}

	return Result{
		Listing: true,
		Verdict: fmt.Sprintf("%d plans in this repository", len(ids)),
		Raw:     strings.Join(ids, "\n"),
		Data:    PlanListData{Plans: ids},
	}
}
