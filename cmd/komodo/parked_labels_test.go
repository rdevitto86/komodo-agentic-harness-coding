package main

import "testing"

func TestBacklogLabelsMapsAGroupIDToItsFolderName(t *testing.T) {
	got := backlogLabels("testdata/backlog-labels")
	if got["019a0000-7c3e-7a41-9b2d-5e8f1c4a6d01"] != "tg-1.1" {
		t.Fatalf("labels %v, want the group id mapped to tg-1.1", got)
	}
}

func TestBacklogLabelsIsEmptyWithNoBacklog(t *testing.T) {
	if got := backlogLabels(t.TempDir()); len(got) != 0 {
		t.Fatalf("labels %v, want none", got)
	}
}
