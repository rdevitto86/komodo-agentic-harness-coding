---
service: tool:agentic-harness-coding
domain: ai
type: prd
status: active
owner: R. DeVitto
depends-on: []
---

# Product requirements — komodo-agentic-factory-coding

| Metadata | Specification |
|---|---|
| Product name | Komodo Agentic Software Assembly Line (`komodo-agentic-factory-coding`) |
| Document version | 1.0.0 |
| Target milestone | Version 1.0.0 LTS, reached through alpha and beta releases |
| Document owner | Engineering Lead / System Architect |
| Status | Approved, 2026-09-25. Open questions hold their defaults. |
| Design | `docs/hld.md`, `docs/lld.md`, `docs/decisions/` |
| Change control | Only the owner edits this file. Line sessions are refused edits; the owner's primary session may edit it on request. |

Planner-facing, and the north star for every other document. Requirement IDs are minted only under Requirements below, never by anything reading this document.

## Executive summary and vision

Komodo is a bolt-on for Claude Code: rules, skills, configuration and compiled Go binaries. It turns a backlog of task groups into reviewed, test-backed pull requests through an eight-stage assembly line.

A mechanical conductor, the `komodo` binary, runs every stage a program can run, and spends zero tokens doing it. Model sessions run only at Build, Review and Repair; each is scoped to its role, capped, and swappable for another model. The primary Claude Code session becomes the orchestrator, the one place a person talks to the line. Every developer gets the same outcome out of the box, and no model session can change `main`.

## Problem statement and objectives

### Problem statement

- **Model subjectivity and drift.** "Done" depends on a model's opinion, so readiness has swung between 72 and 88 out of 100.
- **Environment non-determinism.** A task that succeeds on one macOS setup fails on other developers' machines and operating systems.
- **Unbounded resource spend.** Unconstrained model iterations consume unbounded tokens and loop in review without converging.
- **Guardrail friction.** A bash-parsing guard refused 187 builder commands, fed review loops, and blocks the owner's own edits to this repo.
- **Branch security.** An autonomous agent is a risk when its session holds forge write permission or runs unvetted code against `main`.

### Strategic objectives

- **Deterministic execution.** A compiled conductor runs every stage transition and spends zero orchestration tokens.
- **Bounded time and cost.** A task group finishes in minutes, never more than an hour, and every loop stops when it stops making progress.
- **Platform parity out of the box.** One install command gives the same line on macOS, Linux and Windows.
- **Guaranteed branch safety.** Pull requests open as drafts, the forge credential never reaches a model, and a human merges.
- **Unattended runs.** A plan of a dozen task groups runs for hours without a person. What the line can't settle stops, is written into the backlog, and waits.

## User personas and environments

The personas are examples of who uses the line, not named people.

| Persona | Environment | Primary execution context |
|---|---|---|
| Human engineer | macOS, Linux, or Windows 10 and 11 | Plans work with the orchestrator, runs task groups, reviews and merges PRs |
| Release owner | Any of the above | Everything a human engineer does, plus cutting toolkit releases |
| Autonomous job | Linux, headless | Runs task groups unattended, the way an engineer would, and opens draft PRs |

## Product scope

| Functional area | In scope (1.0.0) | Out of scope (after 1.0.0) |
|---|---|---|
| Host | Claude Code | Codex, other harnesses |
| Model providers | Anthropic models through Claude Code | Ollama and GPT models (1.5 and later), and their logins |
| Target languages | Go, TypeScript | Other languages are unproven, though the line may still run on them |
| Platforms | macOS, Linux, Windows 10 and 11 natively; WSL2 used when present | — |
| Pipeline | The eight stages, and single stages run ad hoc | Cloud facet work, MCP servers |
| Task source | A committed plan: one file per task group in `docs/backlog/`, removed when its epic ends, with `CHANGELOG.md` as the history | Issue trackers and a project platform, through ingest adapters |
| Integrations | Plugin points, shipped disabled: notifiers, tool packs, stage hooks | Enabled Slack, Google Chat and cloud plugins |
| Authentication | The host's own login; the developer's local git PAT for the forge | API-key billing modes, bot accounts |
| Guardrails | OS sandbox where the platform has one, output checks, a guard that catches mistakes, draft-first PRs | A guard that stops an adversarial model |
| Skills | The founding skills in `docs/lld.md#skills-and-scoping`, each scoped to its roles | New standards skills |
| Quality proofs | Programmatic proofs and `komodo eval` | Model-scored rubrics, a local model as a gate |

## Lifecycle workflow

```text
[ Task groups ]
      │
      ▼
┌───────────────────────────┐
│ 1. INGEST                 │  0 tokens: group cards, sizes, checks
└─────────────┬─────────────┘
              ▼
┌───────────────────────────┐
│ 2. COORDINATE             │  0 tokens: parallel groups, worktrees,
└─────────────┬─────────────┘  context packs, pacing
              ▼
┌───────────────────────────┐
│ 3. BUILD                  │  one headless builder per group,
└─────────────┬─────────────┘  in its own worktree
              ▼
┌───────────────────────────┐
│ 4. CHECK                  │◄─────────────┐  0 tokens: format, lint, checks,
└─────────────┬─────────────┘              │  scope, coverage, secret scan
              ▼                            │
┌───────────────────────────┐              │
│ 5. REVIEW                 │              │  parallel lenses; the binary
└─────────────┬─────────────┘              │  verifies every finding
              │                      ┌─────┴───────┐
              ├── verified findings ►│ 6. REPAIR   │  the builder, resumed
              │                      └─────────────┘  with the fix list
              │ none left, or no progress
              ▼
┌───────────────────────────┐
│ 7. PREPARE                │  0 tokens: commit, hooks, rebase,
└─────────────┬─────────────┘  integration build and tests
              ▼
┌───────────────────────────┐
│ 8. SHIP                   │  0 tokens: push with the local PAT;
└─────────────┬─────────────┘  draft PR, labels, ready when verified
              ▼
[ A human reviews and merges ]
```

A task group has 60 minutes from Build to Ship. Repair returns to Check and then to the same review session, and the loop continues only while open findings shrink. The component view is `docs/hld.md#data-flow`.

## Success criteria

V1 ships in three stages: alpha while the rebuild lands, beta once it is feature-complete and only fixes land, and 1.0.0 LTS. The LTS release is ready when all five gates hold. Then the owner cuts the tag:

1. **Requirements coverage.** Every requirement passes its proof with exit code 0.
2. **Benchmark pass rate.** `komodo eval --runs 3` passes at least 90% of golden task groups on macOS, Linux and Windows.
3. **Cross-platform consistency.** At least 90% of golden task groups get identical outcomes on all three platforms from identical starting states.
4. **Unattended execution.** A plan of at least 12 task groups runs to draft PRs in golden repositories with no human input, except to settle groups the line stopped and documented.
5. **Time.** The median task group finishes in 40 minutes or less, and none takes more than 60.

No model's score counts toward 1.0.0. The rollout phases, and the requirements each one proves, are in `docs/lld.md#rollout`.

## Requirements

Each requirement is met only when its proof exits zero. A review can file a bug against a requirement; it cannot mark one met or unmet. Every requirement is Must, because success criterion 1 needs all of them.

### Install and environment

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-1 | One command installs the line on macOS, Linux and Windows, natively or in WSL2: the binary on PATH, the orchestrator layer in the global host config, and the current repo initialised. | Must | The install run and recorded on each platform; `komodo doctor` exits 0 afterwards. |
| REQ-2 | Every machine runs the pinned model IDs, `komodo` release and toolchains. | Must | `komodo doctor` exits 0 when every pin matches, and non-zero when any pin differs. |
| REQ-3 | Line sessions load no personal instructions, user settings, plugins or MCP servers. | Must | A canary instruction in a machine's personal host config never appears in eval output. |
| REQ-4 | Every text file uses LF line endings on every platform. | Must | `.gitattributes` exists in the toolkit and in `komodo init`'s template; `komodo doctor` checks it. |
| REQ-5 | In this repo, a pull that changes the binary's source rebuilds the binary with no manual command. Other repos use a published release. | Must | A test: after a pull that changes Go sources, the binary's build stamp matches the new commit. |
| REQ-6 | Before any session starts, a preflight checks the host login, the forge credential, the sandbox and the budget. A failure stops the run and names the fix. | Must | One eval case per failed check. |

### Ingest

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-7 | Ingest compiles each READY task group into a card with zero model calls, and the same input gives the same card hash. | Must | Unit tests; the ledger shows no session during ingest. |
| REQ-8 | A task group holds 1 to 12 tasks. Ingest refuses a larger group and suggests a split. | Must | Unit tests. |
| REQ-9 | A task needs only a title and its files. Its checks are derived per detected language, and hand-written checks add to them. | Must | Every golden task is written this way and passes `komodo lint`. |
| REQ-10 | A task's status is a checkbox the binary ticks only after the task's checks pass. Nothing else rewrites task text; the conductor only adds or removes a blocker note. | Must | A test: a person's edit to a task body survives a run unchanged. |

### Coordination

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-11 | The binary drives every stage transition; no model session routes stages. | Must | The ledger for an eval run holds only build, review, repair and escalation sessions. |
| REQ-12 | Task groups that share no file run in parallel, up to the concurrency the plan allows. | Must | An eval case: groups that share a file run one after another; groups that share none overlap. |
| REQ-13 | A group branch cuts from its epic branch, or stacks on the branch of a group it declares a dependency on. Its version must equal its epic's exactly. | Must | `komodo lint` rejects any other base, and any group whose version differs from its epic's. |
| REQ-14 | A stopped or killed run resumes without repeating finished work or losing uncommitted work. | Must | An eval case kills a run mid-build, resumes it, and finds every edit and no repeated session. |
| REQ-15 | A run never rebuilds the binary it is running. | Must | A test: a stale build marker during a run changes nothing until the run ends. |

### Build and check

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-16 | Each task group gets one headless builder session in its own worktree, which loads only the builder's skills, tools and hooks. | Must | A test on the rendered builder config; the ledger shows one builder session per group. |
| REQ-17 | After every build or repair session, the binary reruns format, lint, the group's checks, scope, changed-line coverage and a secret scan. | Must | Unit tests; the ledger records no review before these checks pass. |
| REQ-18 | A builder can finish as blocked with a question. The conductor pauses the groups that depend on it and escalates. | Must | A test on the conductor's decision. |

### Review and repair

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-19 | Review runs three lenses in parallel on the group's diff and task list: correctness, security and readiness, and quality. Economy mode runs one combined lens. | Must | The ledger shows the lens sessions for each mode. |
| REQ-20 | A finding blocks only when the binary verifies its evidence: a failing reproducer test, a checklist rule ID on a changed line, or a validator's measurement. | Must | Unit tests, one per kind of evidence. |
| REQ-21 | A re-review resumes the same review session, and may only close its earlier findings or flag lines the repair changed. | Must | A test: a new finding on an unchanged line is dropped. |
| REQ-22 | Repair resumes the builder's session with a task list built only from verified findings. | Must | A test on the repair brief. |
| REQ-23 | A review round that closes no finding ends the loop. The group ships as a draft PR with its open findings listed. | Must | The eval report shows no group with two rounds holding the same open findings. |

### Preparation and shipment

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-24 | Before any push, Preparation commits, runs the pre-commit and pre-push checks, rebases on the base, and runs the integration build and tests. | Must | Unit tests; the ledger shows no push before these pass. |
| REQ-25 | Every PR opens as a draft, or with a `status/wip` label where the forge offers no drafts. It becomes ready for review only when every check and review has passed. | Must | A test for each path. |
| REQ-26 | Shipment is the only stage that uses the forge credential. It pushes only unprotected branches, then labels the PR. | Must | Unit tests; a guard table row for model sessions. |
| REQ-27 | A missing or expired forge credential never loses work. The group stops before Ship with a blocker note, and `komodo ship` finishes it later. | Must | An eval case with the credential removed mid-run. |
| REQ-46 | Cleanup is mechanical. The PR that finishes an epic deletes its group files, a merged group's worktree and branches are removed, `CHANGELOG.md` keeps the history, and doctor flags anything left over. | Must | Unit tests; `komodo doctor` reports each kind of leftover. |

### Time, cost and pacing

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-28 | Every stage and session records its duration, turns, input, output and cached tokens, and cost from the host's own totals, in a local file per run. | Must | Report sums match the host's totals for each session. |
| REQ-29 | A task group finishes within 60 minutes of wall-clock time, with 5 to 40 as the target. At 60 the conductor stops it and escalates. | Must | The eval report shows no group over 60 minutes. |
| REQ-30 | Builders never run on the light tier. On a Pro plan the line uses the economy profile. | Must | `komodo lint` and `komodo doctor` reject a light-tier builder; a test on profile selection. |
| REQ-31 | The conductor spends zero model tokens. | Must | The ledger shows 0 model tokens outside build, review, repair and escalation sessions. |
| REQ-32 | On a subscription plan, the conductor paces to the plan's usage windows, pausing at a limit and resuming at the reset without a person. | Must | An eval case with a simulated rate-limit event. |

### Security and guardrails

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-33 | `main` changes only through a pull request a human merges. Where the forge offers rulesets, one with no bypass actors enforces it. | Must | `komodo doctor --remote` checks the ruleset, or warns that the forge offers none. |
| REQ-34 | No model session holds a forge credential. | Must | An eval case: a session's environment and credential paths contain no forge token. |
| REQ-35 | Every line session runs in the OS sandbox on platforms that have one; the line refuses to run there without it. | Must | A write outside the worktree fails; `komodo run` exits non-zero with the sandbox off. |
| REQ-36 | Check fails on edits outside the group's files, commits made by a model, or changed refs, git hooks or git config. | Must | Unit tests, one per case. |
| REQ-37 | Every hook has one job, one stage and a refusal limit for a line session. Refusals past the limit end the session as blocked instead of looping. | Must | Unit tests per hook; the eval report shows no session past a refusal limit. |
| REQ-38 | Each role's default allow list covers every command its stage needs. The conductor, never an agent, switches branches, syncs with the base and cleans up. | Must | Golden runs record no refusal of an allow-listed command. |

### Orchestrator and harness

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-39 | The primary session can start, watch and stop runs, answer status questions, inject task groups, and run single stages ad hoc, without loading builder or reviewer skills. | Must | A test on the global render; the ledger for an ad hoc stage. |
| REQ-40 | The primary session can edit this repo, including its rules, skills and guard, on a branch; a change takes effect only after a human merges it. | Must | An eval case: an owner-directed edit to `komodo/policy.json` succeeds on a branch. |
| REQ-41 | Line sessions cannot edit this PRD or the golden suite. | Must | A guard table row and a settings deny entry for each. |
| REQ-42 | The plugin points (notifiers, tool packs, stage hooks) exist and ship disabled. | Must | `komodo doctor` lists each one as disabled. |
| REQ-45 | Every escalation goes to the orchestrator first. What it can't settle stops the group: the conductor saves the work, writes a blocker note into the group's backlog file, publishes it as a draft PR labelled `status/blocked`, and waits for a person. A headless run exits non-zero. | Must | One test per path. |
| REQ-47 | Agents act with confidence. On a fully specified task an agent acts without asking and reports a verdict with evidence, with no hedge; it stops only for a critical ref, a remote delete, an external post, or spending money. | Must | An eval case: a fully specified group finishes with no question and no hedge in any result; `komodo lint` holds each always-on rule file to its never budget. |

### Platforms and evaluation

| Requirement ID | Requirement description | Priority | Verification proof |
|---|---|---|---|
| REQ-43 | A timeout kills the whole process tree on every supported platform. | Must | A test: a child that outlives its parent is killed. |
| REQ-44 | The golden suite holds at least 2 real repositories (Go and TypeScript), with at least 10 pinned task groups each and hidden tests. | Must | `komodo eval --list` prints the golden groups and their pinned commits. |

Time limits change only by editing this file. After the first eval, the 5-to-40-minute target moves to what that run measured.

## Constraints

- **Standard library only.** The binary uses only the Go standard library, to limit supply-chain risk.
- **Built on Claude Code.** The line adds rules, skills, hooks and configuration to the host; it never replaces the host.
- **Host authentication.** Claude Code handles its own login, including for autonomous jobs. Hosts added after 1.0.0 must log in during preflight, before any work starts.
- **Forge access.** The forge is GitHub Free, reached with the developer's local git PAT. Draft PRs and rulesets exist there only for public repositories.
- **WSL2 storage location.** When a Windows machine uses WSL2, workspaces live on the Linux filesystem under `~/`, never under `/mnt/c/`.

## Risks and mitigations

| Risk | Severity | Root cause | Mitigation |
|---|---|---|---|
| Endless token-spend loops | High | Unconstrained model iteration on complex or failing work | The progress rule (REQ-23) and the 60-minute group limit (REQ-29) |
| Hooks and guards causing loops | High | Refusals with no way forward, and a guard that judged the line's own commands | One job per hook, refusal limits, and allow lists that cover each stage (REQ-37, REQ-38) |
| Non-deterministic pipeline state | High | A model orchestrating stages and making routing decisions | The conductor makes every routing decision (REQ-11) and spends 0 tokens on control |
| A missing or expired PAT | Medium | The forge credential lives outside the line's control | Preflight (REQ-6), and groups stopped before Ship with a blocker note (REQ-27) |
| Native Windows without an OS sandbox | Medium | The host's sandbox runs only on macOS, Linux and WSL2 | Output checks, the guard, and no forge credential in sessions; WSL2 when present |
| Credential exfiltration by a model | Critical | A model session running commands with forge push rights | No forge token in any model environment (REQ-34) |

## Open questions

The owner resolves each one; its default holds until then. Technical questions are Proposed entries in `docs/decisions/`.

| # | Question | Default if unanswered |
|---|---|---|
| Q1 | Are 90% pass and 90% consistency the right bars? | Yes |
| Q2 | What dollar budget per run, once eval shows what a group costs? | None until the first eval |

The golden repos are the owner's pick at the time of the eval; the owner also runs the line ad hoc in any repo.
