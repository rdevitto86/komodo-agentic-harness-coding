---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0009. The line's mode follows the subscription

**Status:** Accepted, 2026-10-02.

**Context.** A user's plan decides how much the line can spend. One setting for every plan either wastes a large plan or hits a small one's limits mid-group.

**Decision.**

- **The conductor reads the plan and picks a mode.** A Pro plan runs economy mode; every other plan runs full mode.
- **Economy mode spends less:** a standard-tier builder and one combined review lens.
- **Full mode spends for fewer repair rounds:** a heavy-tier builder and parallel review lenses.
- **On a subscription, the conductor sets concurrency from the plan,** pauses at a usage limit, and resumes at the reset.
- **On API billing, a spend budget per run applies.**

`lld.md` and `komodo/profiles/` hold each mode's models and efforts.

**Consequences.**

- **Economy mode trades some review depth for cost,** and eval measures how much.

**Open.** The API spend budget isn't built; `internal/preflight/preflight.go` holds a TODO.
