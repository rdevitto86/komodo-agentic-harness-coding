---
service: tool:agentic-harness-coding
domain: ai
type: decision
status: active
owner: R. DeVitto
depends-on: []
---

# 0011. Specs are prd, hld, lld and one file per decision

**Status:** Accepted, 2026-10-02.

**Context.** The four spec files were `prd.md`, `architecture.md`, `system-design.md` and `decisions.md`. People and agents confused the middle two: both describe the design, and the names do not say where one ends. The decision log was one append-only file, so two branches that each added a decision always conflicted at its end, the same failure that moved the backlog to one file per group (0004). Repos on the older PRD and SDD pair mixed slow structure with code-paced detail in one file, and buried decisions in a table.

**Decision.** A repo's specs are `docs/prd.md`, `docs/hld.md`, `docs/lld.md` and `docs/decisions/`, one file per decision named `NNNN-<slug>.md`. The high-level design holds what would survive a rewrite in another language: purpose, context, components, boundaries, data flow, the C4 context and container levels. The low-level design holds what changes with the code: data model, interfaces, operations, recovery and testing, the component level and below. `komodo lint` refuses two decision files with one number. Beside them, `docs/diagrams/` holds each diagram's editable source next to its export, and `docs/media/` the static images, videos and GIFs a doc shows.

**Alternatives.**

- **Keep `architecture.md` and `system-design.md`.** The split was right and the names were not.
- **One design file.** Mixes a yearly change rate with a per-task one, so every PR touches the file every reader starts from.
- **Per-feature design docs or RFCs.** They go stale after launch and leave no single current answer; a proposal is a Proposed decision.
- **Keep one decisions file.** Parallel branches conflict on every new entry.

**Consequences.**

- **Parallel branches add decisions without conflict;** a duplicate number fails lint and the later branch renumbers.
- **Agents read the prose, people also see the pictures;** a diagram never holds a fact the prose lacks.
- **HLD and LLD are the terms the wider industry already uses,** so a new reader needs no glossary.
- **Every repo on the older names migrates:** `architecture.md` to `hld.md`, `system-design.md` or an SDD to `lld.md`, and its decisions to `docs/decisions/`.
