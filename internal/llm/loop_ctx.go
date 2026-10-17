package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/state"
)

// stubPrefix starts every stub, so a stub is never stubbed again.
const stubPrefix = "[output of "

// tokens is the larger of the size estimate and the last reported size plus the messages added
// since.
func (l *loopState) tokens() int {
	est := estimateTokens(l.msgs)
	if l.lastIn > 0 && l.sentLen <= len(l.msgs) {
		return max(est, l.lastIn+estimateTokens(l.msgs[l.sentLen:]))
	}

	return est
}

// rebuildInfo restores the tool names and call numbers of a saved conversation; the host's log
// paths are not kept.
func (l *loopState) rebuildInfo() {
	names := map[string]string{}

	for _, m := range l.msgs {
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				names[b.ID] = b.Name
			case "tool_result":
				l.calls++

				name := names[b.ToolUseID]
				if name == "" {
					name = "tool"
				}

				l.info[b.ToolUseID] = toolInfo{name: name, n: l.calls}
			}
		}
	}
}

// fitContext stubs the oldest tool outputs when the conversation passes min(200,000, 80% of the
// context), down to half of that. It fails with no_result when the conversation still exceeds the
// context, so nothing past the context is sent.
func (l *loopState) fitContext() error {
	ctxTokens := l.cfg.Session.Profile.Ctx
	if ctxTokens <= 0 {
		ctxTokens = DefaultWindow
	}

	// maxContextTokens caps the conversation size before old tool outputs are stubbed.
	const maxContextTokens = 200000

	limit := min(maxContextTokens, ctxTokens*8/10)

	n := l.tokens()
	if n > limit {
		target := limit / 2

	stubbing:
		for i := range l.msgs {
			for j := range l.msgs[i].Content {
				b := &l.msgs[i].Content[j]
				if b.Type != "tool_result" ||
					n <= target ||
					strings.HasPrefix(b.Content, stubPrefix) {
					continue
				}

				info, known := l.info[b.ToolUseID]
				if !known {
					info = toolInfo{name: "tool", n: i + 1}
				}

				stub := stubText(info.name, info.n, len(b.Content), info.log)
				if len(stub) >= len(b.Content) {
					continue
				}

				saved := (len(b.Content) - len(stub)) / 4
				b.Content = stub
				n -= saved
				l.lastIn = max(0, l.lastIn-saved)

				if n <= target {
					break stubbing
				}
			}
		}
	}

	if n > ctxTokens {
		return &EndError{
			Reason: state.ReasonNoResult,
			Detail: fmt.Sprintf(
				"the conversation holds about %d tokens, past the context of %d",
				n,
				ctxTokens,
			),
		}
	}

	return nil
}

// estimateTokens approximates the tokens of a conversation as its encoded size divided by 4,
// rounded up.
func estimateTokens(msgs []message) int {
	n := 0

	for _, m := range msgs {
		b, _ := json.Marshal(m)
		n += (len(b) + 3) / 4
	}

	return n
}

// stubText is what replaces a cut tool output.
func stubText(tool string, n, size int, log string) string {
	if log == "" {
		return fmt.Sprintf("[output of %s call %d cut: %d bytes, not saved]", tool, n, size)
	}

	return fmt.Sprintf("[output of %s call %d cut: %d bytes in %s]", tool, n, size, log)
}
