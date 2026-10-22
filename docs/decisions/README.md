---
service: tool:agentic-harness-coding
domain: ai
type: index
status: draft
owner: R. DeVitto
depends-on: []
---

# Decisions — komodo-agentic-factory-coding

Why the line is this way and not another. One file per decision, named `NNNN-<slug>.md` from its title, numbered in order and never rewritten. A superseded entry keeps its text and gains a status line naming its successor. An open technical question is a Proposed entry; a product question belongs in `prd.md`.

This log starts fresh with V1 on 2026-09-25. The prototype's decisions are `docs/design-decisions.md` at the tag `prototype-final`, and the first 1.0.0 line's are in git history under this directory before 2026-09-25; the ones that still hold are restated here. From then until 2026-10-02 the log was one file, `docs/decisions.md`. On 2026-10-02 its 36 entries were consolidated into 11 and renumbered from 0001; the earlier numbers and text are in git history, and `CHANGELOG.md`'s released sections still cite them.

`komodo lint` refuses two files with one number; the branch that lands second takes the next free one.
