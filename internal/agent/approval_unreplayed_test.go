package agent

import "testing"

// An unreplayed pass is handled like a first dispute: the round waits for the restart, and a second one blocks qa.
func TestDecideUnreplayedPass(t *testing.T) {
	pass := ReviewerVerdict{Ended: true}
	first := Decide(QAVerdict{Ended: true, Unreplayed: []string{"ac-1"}}, pass)
	if first.Decision != DecisionWait {
		t.Errorf("first unreplayed = %+v", first)
	}

	second := Decide(QAVerdict{Ended: true, Restarts: 1, Unreplayed: []string{"ac-1"}}, pass)
	if second.Decision != DecisionBlock || second.Reason != BlockQA {
		t.Errorf("second unreplayed = %+v", second)
	}
}
