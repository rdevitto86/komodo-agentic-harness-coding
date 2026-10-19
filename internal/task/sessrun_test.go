package task

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/llm"
)

// linkRig builds a rig whose only chain link is l, with a passing tester and reviewer queued.
func linkRig(t *testing.T, l llm.Link) *pipeRig {
	t.Helper()
	r := newPipeRig(t, rigOpts{chain: []string{string(l)}})
	r.model.on("tester", sayResult(testerPass))
	r.model.on("reviewer", sayResult(reviewerMet))
	return r
}

func TestProgramLinksGetAnMCPConfigAndASessionHandle(t *testing.T) {
	for _, tc := range []struct {
		link     llm.Link
		wantMCP  bool
		wantKind string
	}{
		{llm.ClaudeCLI, true, "session_id"},
		{llm.OpenAISubscription, true, "session_id"},
		{llm.AnthropicAPI, false, "messages_file"},
	} {
		r := linkRig(t, tc.link)
		mcp, kind := false, ""
		r.model.on("builder", func(s llm.Session) (llm.Stream, error) {
			_, err := os.Stat(filepath.Join(s.Dir, "mcp.json"))
			mcp = err == nil
			r.m.mu.Lock()
			kind = r.m.prog.Handles["builder"].Kind
			r.m.mu.Unlock()
			return building(r.wt, "a.go", "package a\n", builderDone)(s)
		})
		r.reachCleanup()
		if mcp != tc.wantMCP || kind != tc.wantKind {
			t.Errorf("%s: mcp.json %v (want %v), handle kind %q (want %q)", tc.link, mcp, tc.wantMCP, kind, tc.wantKind)
		}
	}
}

func TestRejectedResultIsSentBackAsAResumeOnProgramLinks(t *testing.T) {
	for _, tc := range []struct {
		link       llm.Link
		wantResume int
	}{
		{llm.ClaudeCLI, 1},
		{llm.OpenAISubscription, 1},
		{llm.AnthropicAPI, 0},
	} {
		r := linkRig(t, tc.link)
		resumed := 0
		r.model.resume = func(llm.Session, llm.Input) (llm.Stream, error) {
			resumed++
			return building(r.wt, "a.go", "package a\n", builderDone)(llm.Session{})
		}
		r.model.on("builder", sayResult(`{"status":"bogus"}`))
		// A link without a resume gets a second session instead.
		r.model.on("builder", building(r.wt, "a.go", "package a\n", builderDone))
		r.reachCleanup()
		if resumed != tc.wantResume {
			t.Errorf("%s: %d resumes, want %d", tc.link, resumed, tc.wantResume)
		}
	}
}
