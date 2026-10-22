---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0003. The assembly line and ad hoc work have separate entries and rules

**Status:** Accepted, 2026-10-02.

**Context.** The primary session is where a person talks to the line, and most requests are not a task group: a question, an exploration, a one-off fix. When the two shared one path, the line's refusal limits ended the orchestrator's own session, and a model relayed `/run` turn by turn.

**Decision.**

| | Assembly line | Ad hoc |
|---|---|---|
| Entry | `/run`, which runs `komodo run` | The orchestrator spawns its own agents |
| Driven by | The binary, stage by stage | The orchestrator |
| Unit of work | A READY task group from `docs/backlog/` | Any request |
| Guard | Global and line tiers (0008) | Global tier only |
| Git | The conductor commits, pushes and opens PRs | The agent works its own branch and opens a PR with `komodo pr create` |
| Output | A reviewed draft PR into the epic branch | Whatever the request needs |

- **The orchestrator also plans, starts and watches runs, and settles escalations.** It never relays a stage.
- **Use the line for repeatable work that needs proof;** use ad hoc for everything else.

**Alternatives.**

- **An `adhoc` skill and `komodo stage`.** They duplicated the orchestrator's own ability to spawn an agent.
- **No orchestrator.** Nobody could settle a blocked group without a person.

**Consequences.**

- **The orchestrator runs parallel agents without tripping the line's limits.**
