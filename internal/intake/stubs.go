package intake

func Load(root string) ([]Epic, []Finding, error)                                   { return nil, nil, nil }
func Lint(epics []Epic, protected, tracked []string) []Finding                      { return nil }
func LintDraft(d PlanDraft, existing []Epic, protected, tracked []string) []Finding { return nil }
func OverEstimate(t Task, actualLines int) (Finding, bool)                          { return Finding{}, false }
func CardHash(root string, g Group) (string, error)                                 { return "", nil }
func BuildGraph(epics []Epic, tracked []string) (Graph, []Finding)                  { return Graph{}, nil }
func (g Graph) Waves() [][]string                                                   { return nil }
func (g Graph) LongestChain() int                                                   { return 0 }
func Pending(epics []Epic, g Graph, target string) []string                         { return nil }

// PlanDraft is the planner's result: every task-list file of the plan, in memory.
type PlanDraft struct {
	Files []DraftFile `json:"files"`
}

// DraftFile is one task-list file of a draft.
type DraftFile struct {
	Path    string `json:"path"`    // path under docs/backlog/, such as epic-2/tg-2.1/TG.md
	Content string `json:"content"` // the whole file
}

func sortFindings(f []Finding) []Finding { return f }
