package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/agent"
)

// toolSubmitFinding files one finding; the role judges its evidence later.
func toolSubmitFinding(_ context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a findingArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if strings.TrimSpace(a.Rule) == "" || strings.TrimSpace(a.Quote) == "" || a.Line < 1 {
		return errReply("a finding needs a rule, a line from 1 and the quoted line")
	}

	if a.Confidence == nil || !agent.ValidConfidence(*a.Confidence) {
		return errReply("a finding needs a confidence from 0 to 100")
	}

	r, e := locate(tc, a.File)
	if e != nil {
		return *e
	}

	f := Submitted{
		Finding: agent.Finding{
			Rule: a.Rule, File: r.Rel, Line: a.Line, Quote: a.Quote, Proof: a.Proof, Confidence: *a.Confidence,
		},
		Note: a.Note,
	}
	fact := map[string]any{
		"rule":       f.Rule,
		"file":       f.File,
		"line":       f.Line,
		"quote":      f.Quote,
		"note":       f.Note,
		"confidence": f.Confidence,
	}
	if f.Proof != nil {
		fact["proof"] = map[string]string{"ran": f.Proof.Ran, "saw": f.Proof.Saw}
	}
	data, _ := json.Marshal(fact)
	tc.bound.line(ToolLine{Kind: "finding", Call: tc.id, Tool: "submit_finding", Data: data})
	tc.bound.fact(Fact{Subtype: "finding.submit", At: tc.env.now(), Data: data})
	if tc.env.OnFinding != nil {
		tc.env.OnFinding(f)
	}

	return Reply{Text: "filed " + f.Rule + " at " + f.File + ":" + strconv.Itoa(f.Line)}
}

// toolTaskDone reruns the task's done_when command through the pipeline and reports its exit code and output.
func toolTaskDone(ctx context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a doneArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if tc.env.Done == nil {
		return errReply("task_done is not available in this session")
	}

	exit, out, err := tc.env.Done(ctx, a.Task)
	if err != nil {
		return errReply("%v", err)
	}

	return Reply{Text: fmt.Sprintf("exit code %d\n%s", exit, out)}
}

// toolSkill returns the text of a skill or one of its sections.
func toolSkill(_ context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a skillArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if tc.env.Skill == nil {
		return errReply("skills are not available in this session")
	}

	text, err := tc.env.Skill(a.Name)
	if err != nil {
		return errReply("%v", err)
	}

	return Reply{Text: text}
}

// toolLintPlan returns the lint errors of a plan text, or ok.
func toolLintPlan(_ context.Context, tc *toolCall, raw json.RawMessage) Reply {
	var a lintArgs
	if err := decode(raw, &a); err != nil {
		return bad(err)
	}
	if tc.env.Lint == nil {
		return errReply("lint_plan is not available in this session")
	}
	if errs := tc.env.Lint(a.Draft); len(errs) > 0 {
		return Reply{Text: strings.Join(errs, "\n")}
	}

	return Reply{Text: "ok"}
}
