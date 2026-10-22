---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0005. Review sends a group back to repair until it converges, or blocks it

**Status:** Accepted, 2026-10-02.

**Context.** Review is where the line backtracks. In the first line it never converged: one group ran 11 rounds because each round re-reviewed the whole diff from scratch and raised new objections.

**Decision.**

- **Check runs first, with no model:** the group's checks and the validators.
- **Review runs lenses in parallel, each a fresh session that never saw the build:** correctness, security and readiness, and quality. In full mode the first three run on the heavy tier at high effort, and quality on the standard tier; economy mode runs one combined lens.
- **A finding blocks only with evidence the conductor verifies.** An unverified finding is reported and never blocks.
- **Approve:** when no lens holds an open verified finding, the group moves to Prepare and Ship.
- **Send back:** verified findings become one fix list, and Repair resumes the builder with it. Re-review resumes each lens's own session, which may only close its findings or flag lines the repair changed.

**The cold pass.** Once a lens has run 2 warm rounds and every lens passes, that lens gets one fresh session over the final diff. A warm reviewer anchors on its own earlier findings; one fresh look catches what it stopped seeing, and only one, so it can't restart the loop.

**When it can't converge.** A round that closes nothing ends the loop and escalates to the orchestrator. What it can't settle goes BLOCKED: a note in the group's backlog file, on its branch, published as a draft PR labelled `status/blocked`. Other groups continue, and `komodo resume` picks it up once a person marks it READY.

**Alternatives.**

- **A fresh reviewer every round.** It raises new objections each round, as in the 11-round group.
- **A fixed repair count.** Arbitrary, and it defers the loop rather than ending it.
- **One reviewer for every group.** Its context grows, and it stops being independent.

**Consequences.**

- **Only verified findings send work back,** so reviewer disagreement can't cause a loop.
- **The blocked draft PR is the one place a person looks.**
