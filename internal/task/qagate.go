package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// runDeep sets the group's risk level from its diff, logs the rule, and runs the deep checks of that level with no model
// call; it returns the findings of the checks that failed.
func (m *Manager) runDeep(ctx context.Context, run agent.CommandRunner) ([]string, error) {
	if m.c.Git == nil {
		return nil, nil
	}

	diff, err := m.c.Git.Diff(ctx, m.spec.Lease, nil)
	if err != nil {
		return nil, err
	}

	d := m.classify(diff)

	m.mu.Lock()
	m.risk = d
	m.mu.Unlock()
	m.fact("risk.set", bus.RiskSet{Level: string(d.Level), Reason: d.Reason})

	if m.c.Deep == nil {
		return nil, nil
	}

	results, err := m.c.Deep.Run(ctx, run, agent.DeepPlan(d.Level))
	if err != nil {
		return nil, block("setup", map[string]string{"error": err.Error(), "step": "deep checks"})
	}

	done := bus.DeepDone{Pass: true}

	var fails []string

	for _, r := range results {
		c := bus.DeepCheck{Kind: string(r.Kind), Pass: r.Pass}

		for _, mu := range r.Mutants {
			if mu.Status == agent.MutantSurvived {
				c.Mutants++
			}
		}

		if r.Pass {
			c.Note = firstLine(r.Output)
		} else {
			done.Pass = false

			fails = append(fails, fmt.Sprintf("the %s deep check failed\n%s", r.Kind, r.Output))
		}

		done.Results = append(done.Results, c)
	}

	m.fact("deep.done", done)

	return fails, nil
}

// classify sets the Go-rule risk level of diff, raised when an earlier round proved a reviewer finding.
func (m *Manager) classify(diff string) agent.RiskDecision {
	m.mu.Lock()
	proven := m.proven
	m.mu.Unlock()

	return agent.Classify(agent.RiskInput{
		Files:     agent.DiffFiles(diff),
		Proven:    proven,
		PublicAPI: agent.PublicAPIChanged(diff),
	})
}

// skipsReview reports whether the round takes no Reviewer: the skip rule says so and no finding was proven yet.
func (m *Manager) skipsReview(diff string) bool {
	m.mu.Lock()
	proven := m.proven
	m.mu.Unlock()

	return !proven && m.c.SkipReview != nil && m.c.SkipReview(diff)
}

// noteProven marks the group once a reviewer finding blocks: it carries a proof and a confidence of 80 or more.
func (m *Manager) noteProven(vs []VerifiedFinding) {
	for _, v := range vs {
		if v.Bucket == agent.BucketBlock {
			m.mu.Lock()
			m.proven = true
			m.mu.Unlock()

			return
		}
	}
}

// Risk returns the risk level and the rule Go set for the group in its last round.
func (m *Manager) Risk() agent.RiskDecision {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.risk
}

// stampSignoff records Quality Assurance's accepted pass over the tree and the test files it judged.
func (m *Manager) stampSignoff(ctx context.Context) error {
	if t, ok := m.verdictOf("qa"); ok && len(t.FailOpen) > 0 {
		return nil
	}

	tree, err := m.c.Git.TreeHash(ctx, m.spec.Lease)
	if err != nil {
		return err
	}

	hash, err := m.testsHash(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.signoff = &agent.Signoff{Tree: tree, TestsHash: hash}
	m.mu.Unlock()

	return nil
}

// Signoff returns the group's sign-off; false before Quality Assurance has passed.
func (m *Manager) Signoff() (agent.Signoff, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.signoff == nil {
		return agent.Signoff{}, false
	}

	return *m.signoff, true
}

// SignoffVoided reports whether a test file changed since the sign-off.
func (m *Manager) SignoffVoided(ctx context.Context) bool {
	s, ok := m.Signoff()
	if !ok || m.c.Git == nil {
		return false
	}

	hash, err := m.testsHash(ctx)

	return err != nil || s.Voided(hash)
}

// voidSignoff drops a sign-off whose test files changed, and Quality Assurance's stored pass with it.
func (m *Manager) voidSignoff(ctx context.Context) {
	if !m.SignoffVoided(ctx) {
		return
	}

	m.mu.Lock()
	m.signoff = nil
	delete(m.verdicts, "qa")
	m.mu.Unlock()
}

// testsHash hashes the group's changed test files as they are in the worktree.
func (m *Manager) testsHash(ctx context.Context) (string, error) {
	diff, err := m.c.Git.Diff(ctx, m.spec.Lease, nil)
	if err != nil {
		return "", err
	}

	files := map[string][]byte{}

	for _, p := range diffPaths(diff) {
		if !agent.IsTestFile(p) {
			continue
		}

		b, err := os.ReadFile(filepath.Join(m.spec.Lease.Worktree, filepath.FromSlash(p)))
		if err == nil {
			files[p] = b
		}
	}

	return agent.TestsHash(files), nil
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")

	return l
}
