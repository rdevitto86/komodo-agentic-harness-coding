---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0001. Work runs through an assembly line of fixed stages, in isolated, pinned sessions

**Status:** Accepted, 2026-10-02.

**Context.** V1 needs four things: the same result from the same task on any machine, a bounded cost per change, review that ends, and proof that never rests on a model's word. When a model steers its own loop, it skips stages, spends tokens nobody meters, and grades its own work. When a session loads whatever its machine has, three machines run three different agents.

**Decision.**

- **Work moves through eight fixed stages:** Ingest, Coordinate, Build, Check, Review, Repair, Prepare and Ship. The binary runs them in order. Models work only inside Build, Review and Repair, and Coordinate calls one only to settle an escalation.
- **Each stage's output is data checked against a schema,** and the next stage starts from what is on disk, so a run stops and resumes at any stage.
- **Every session the line starts is isolated.** It ignores the person's own instructions, settings, plugins and MCP servers, sees only its role's skills, and runs under a turn cap.
- **Every session is pinned.** It runs an exact model ID, never an alias, and the host's autoupdater is off for its lifetime. The host CLI itself is not pinned; preflight checks only that it runs and holds a login.
- **Readiness is measured by `komodo eval`:** real task groups in real repos, with hidden tests, against the PRD's success criteria. A model's score is never a proof.

**Alternatives.**

- **A model drives the loop.** It skips stages, and one bad turn ships unreviewed work.
- **One agent per change with no stages.** Nothing independent checks it.
- **A rubric a model scores.** The same code scored between 72 and 88.

**Consequences.**

- **The normal path spends no tokens on orchestration.**
- **Every machine runs the same agents,** and a host update shows up as a failed session, not a doctor finding.
- **`hld.md` and `lld.md` hold how each stage works;** this decision holds why there are stages.
