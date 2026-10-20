package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/commit"
)

// recheckSlot carries the verdict the Task Manager hands to its recheck callback back to the waiting caller.
type recheckSlot struct {
	mu   sync.Mutex
	pass bool
	out  string
	got  bool
}

// deliver records the verdict of one recheck.
func (r *recheckSlot) deliver(pass bool, out string) {
	r.mu.Lock()
	r.pass, r.out, r.got = pass, out, true
	r.mu.Unlock()
}

// take returns the recorded verdict and clears the slot.
func (r *recheckSlot) take() (pass bool, out string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pass, out, ok = r.pass, r.out, r.got
	r.got = false
	return pass, out, ok
}

// groupChecker runs the free checks of a landing through the group's Task Manager, in a sandbox made for the recheck.
type groupChecker struct {
	ref      *groupRef
	worktree string // the group's own worktree
}

// Recheck runs the checks after the group's landing, when the landing worktree's tree must be the group's tree and a
// difference is an error; before the landing the checks pass, because the group's sandbox cannot see that tip.
func (g groupChecker) Recheck(ctx context.Context, _, landing string, landed bool) (commit.CheckResult, error) {
	if !landed {
		return commit.CheckResult{Pass: true}, nil
	}

	groupTree, err := gitOut("-C", g.worktree, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return commit.CheckResult{}, fmt.Errorf("read the group's tree: %w", err)
	}

	epicTree, err := gitOut("-C", landing, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return commit.CheckResult{}, fmt.Errorf("read the epic tip's tree: %w", err)
	}
	if groupTree != epicTree {
		return commit.CheckResult{}, errors.New(
			"the group's checkout differs from the epic tip, so its sandbox cannot check the tip",
		)
	}

	tm := g.ref.manager()
	if tm == nil {
		return commit.CheckResult{}, errors.New("the group has no task manager to run the checks")
	}

	g.ref.recheckMu.Lock()
	defer g.ref.recheckMu.Unlock()

	g.ref.slot.take()
	tm.Recheck(ctx)
	// The checks leave files such as the coverage profile in the checkout, and cleanup refuses to remove a checkout holding
	// files the tip lacks.
	_, cleanErr := gitOut("-C", g.worktree, "clean", "-fdq")
	pass, out, ok := g.ref.slot.take()
	if cleanErr != nil {
		return commit.CheckResult{}, fmt.Errorf("remove the files the checks left: %w", cleanErr)
	}
	if !ok {
		return commit.CheckResult{}, errors.New("the task manager returned no check result")
	}

	return commit.CheckResult{Pass: pass, Output: strings.TrimSpace(out)}, nil
}
