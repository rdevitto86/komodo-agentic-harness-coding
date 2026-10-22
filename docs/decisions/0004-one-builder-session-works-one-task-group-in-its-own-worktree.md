---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0004. One builder session works one task group, in its own worktree

**Status:** Accepted, 2026-10-02. Amended by 0012, 2026-10-02.

**Context.** A task group is the size of one engineering story: a task list for one agent, checked by one review. A session per task pays the fixed prompt again for every task, and the reviewer sees fragments of one story.

**Decision.**

- **A group holds 1 to 12 tasks,** and `komodo lint` refuses more.
- **Each group gets one worktree, and one builder session is bound to that worktree,** not to the group file. Repair resumes the same session in the same worktree.
- **Ingest compiles each READY group into a card with no model calls:** tasks, files, derived checks, context and base. The conductor ticks a task's box only after its checks pass.
- **Each group is a committed file in `docs/backlog/`,** and its finished file goes when its epic ends.
- **Groups that share no file run in parallel.**

**Alternatives.**

- **One session per task.** It repeats the fixed prompt and splits the story.
- **One `BACKLOG.md`.** It became a database that every branch conflicted over.

**Consequences.**

- **One group is one pull request.**
- **The same card and tree give the same brief** on every machine.
