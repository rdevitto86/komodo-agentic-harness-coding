package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEveryRowAnswersHelpJSON(t *testing.T) {
	for _, row := range tableRows {
		t.Run(row.Name, func(t *testing.T) {
			h := newHarness(t)
			args := append(strings.Fields(row.Name), "--help", "--json")
			code, out, errOut := h.run(args...)
			if code != 0 {
				t.Fatalf("exit %d\nstdout=%s\nstderr=%s", code, out, errOut)
			}
			var docs []struct {
				Name   string         `json:"name"`
				Result map[string]any `json:"result"`
			}
			if err := json.Unmarshal([]byte(out), &docs); err != nil || len(docs) != 1 {
				t.Fatalf("help is not one command document: %v\n%s", err, out)
			}
			if docs[0].Name != row.Name || docs[0].Result["$schema"] == nil {
				t.Fatalf("help = %+v", docs[0])
			}
		})
	}
}
