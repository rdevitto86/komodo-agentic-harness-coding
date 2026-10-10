# Komodo 1.0.0-beta.6 — High-Level Design

## 1. Table of contents

- [2. Glossary](#2-glossary)
- [3. Document metadata](#3-document-metadata)
  - [3.1 Owners](#31-owners)
  - [3.2 Current controls](#32-current-controls)
  - [3.3 Revision history](#33-revision-history)
  - [3.4 Related documents](#34-related-documents)
- [4. Introduction and goals](#4-introduction-and-goals)
  - [4.1 Purpose](#41-purpose)
  - [4.2 Scope](#42-scope)
  - [4.3 Business drivers](#43-business-drivers)
  - [4.4 Requirements overview](#44-requirements-overview)
  - [4.5 Goals and non-goals](#45-goals-and-non-goals)
  - [4.6 Quality goals](#46-quality-goals)
  - [4.7 Stakeholders and concerns](#47-stakeholders-and-concerns)
- [5. Architecture constraints](#5-architecture-constraints)
  - [5.1 Technical constraints](#51-technical-constraints)
  - [5.2 Organizational constraints](#52-organizational-constraints)
  - [5.3 Conventions](#53-conventions)
- [6. Context and scope](#6-context-and-scope)
  - [6.1 Business context](#61-business-context)
  - [6.2 Technical context](#62-technical-context)
  - [6.3 Inputs and outputs](#63-inputs-and-outputs)
- [7. Solution strategy](#7-solution-strategy)
  - [7.1 Key decisions](#71-key-decisions)
  - [7.2 Technology choices](#72-technology-choices)
  - [7.3 Precedents](#73-precedents)
- [8. Building block view](#8-building-block-view)
  - [8.1 Level 1: layers](#81-level-1-layers)
  - [8.2 Level 2: components](#82-level-2-components)
  - [8.3 Component diagram](#83-component-diagram)
  - [8.4 Dependency rules](#84-dependency-rules)
  - [8.5 Concepts to components](#85-concepts-to-components)
- [9. Runtime view](#9-runtime-view)
  - [9.1 Scenario: the event spine](#91-scenario-the-event-spine)
  - [9.2 Scenario: a group's lifecycle](#92-scenario-a-groups-lifecycle)
  - [9.3 Scenario: the Verify race](#93-scenario-the-verify-race)
  - [9.4 Scenario: run order across groups](#94-scenario-run-order-across-groups)
  - [9.5 Scenario: the session pool](#95-scenario-the-session-pool)
  - [9.6 Scenario: one session, end to end](#96-scenario-one-session-end-to-end)
  - [9.7 Scenario: from goal to merged epic PR](#97-scenario-from-goal-to-merged-epic-pr)
- [10. Data view](#10-data-view)
  - [10.1 Where data lives](#101-where-data-lives)
  - [10.2 Data flow](#102-data-flow)
  - [10.3 Configuration model](#103-configuration-model)
  - [10.4 Cache](#104-cache)
  - [10.5 Observability data](#105-observability-data)
- [11. Model and provider view](#11-model-and-provider-view)
  - [11.1 Links](#111-links)
  - [11.2 Weights and models](#112-weights-and-models)
  - [11.3 The chain and failover](#113-the-chain-and-failover)
  - [11.4 Cost classes and the billing check](#114-cost-classes-and-the-billing-check)
  - [11.5 Limits, pauses and warnings](#115-limits-pauses-and-warnings)
- [12. Agent roster](#12-agent-roster)
- [13. Deployment view](#13-deployment-view)
  - [13.1 Platforms](#131-platforms)
  - [13.2 Topology](#132-topology)
  - [13.3 Install, update and health](#133-install-update-and-health)
- [14. Security view](#14-security-view)
  - [14.1 Threats and controls](#141-threats-and-controls)
  - [14.2 Guard summary](#142-guard-summary)
  - [14.3 Sandbox summary](#143-sandbox-summary)
  - [14.4 Release integrity](#144-release-integrity)
- [15. Quality assurance view](#15-quality-assurance-view)
  - [15.1 Four gates, three of them free](#151-four-gates-three-of-them-free)
  - [15.2 Check (ground truth)](#152-check-ground-truth)
  - [15.3 Review and the tester](#153-review-and-the-tester)
  - [15.4 Gate and self-checks (Komodo's own repo)](#154-gate-and-self-checks-komodos-own-repo)
- [16. Operations view](#16-operations-view)
  - [16.1 Scheduling](#161-scheduling)
  - [16.2 Error recovery and the loss bound](#162-error-recovery-and-the-loss-bound)
  - [16.3 Observability](#163-observability)
  - [16.4 Runbook summary](#164-runbook-summary)
- [17. Interfaces](#17-interfaces)
  - [17.1 Command surface](#171-command-surface)
  - [17.2 Envelope and exit codes](#172-envelope-and-exit-codes)
  - [17.3 The orchestrator contract](#173-the-orchestrator-contract)
- [18. Architecture decisions](#18-architecture-decisions)
  - [18.1 Decisions the owner made (2026-10-09)](#181-decisions-the-owner-made-2026-10-09)
  - [18.2 Decisions recorded with their rejected alternative](#182-decisions-recorded-with-their-rejected-alternative)
  - [18.3 Defaults chosen by the design pass, awaiting the owner's confirmation](#183-defaults-chosen-by-the-design-pass-awaiting-the-owners-confirmation)
- [19. Quality requirements](#19-quality-requirements)
  - [19.1 Quality scenarios](#191-quality-scenarios)
  - [19.2 Quality tree](#192-quality-tree)
- [20. Risks and technical debt](#20-risks-and-technical-debt)
  - [20.1 Risks](#201-risks)
  - [20.2 Technical debt](#202-technical-debt)
- [21. Delivery roadmap](#21-delivery-roadmap)
- [Appendix A. Traceability](#appendix-a-traceability)
- [Appendix B. References](#appendix-b-references)

---

## 2. Glossary

| Term | Meaning |
|---|---|
| Harness | The `komodo` binary: the engine that plans, builds, checks, reviews and ships code using AI model sessions as workers. |
| Orchestrator | The interactive AI session that drives the harness through CLI verbs. Claude Code in beta 6. It never edits code itself. |
| Worker, agent | A stateless model session with a private toolset. Seven agents exist: planner, architect, reviewer, builder, researcher, tester, scout. |
| Session | One fresh model conversation for one task. It has a brief, a tool server, a sandbox profile and a 60-minute wall clock. |
| Epic, group, task | The backlog hierarchy: an epic holds task groups; a group holds up to 8 tasks; a task names at most 5 files. |
| Stage | One of five pipeline steps a group moves through: Intake, Build, Check, Verify, Ship. Paused and Blocked are holding states. |
| Event | A typed fact the engine records, such as `task.done`. Thirty-two types exist. |
| Journal | The append-only event file per group. State is a replayable product of it. |
| `Next()` | The one pure function that turns (state, event) into (new state, actions, follow-on events). Nothing else changes group state. |
| Outbox | The journal entries `action.start` and `action.done` that bracket every side effect so a crash can replay it safely. |
| Epoch | An integer that rises on every stage entry. A session event carrying an older epoch is stale and ignored. |
| Lease | A per-group lock file on the machine, held from Intake to landing, renewed every 5 minutes, expiring 2 hours after the last renewal. |
| Fence | A monotonic integer beside the lease. Every git write carries it, and git's compare-and-swap refuses a stale holder. |
| Worktree | A detached git checkout per group under `~/.komodo/wt/`, where the builder works. |
| Hidden ref | `refs/komodo/groups/<group>`, where the engine saves every group commit. Never pushed, hidden from `git branch`. |
| Link | A way to reach a model: `claude-cli`, `anthropic-api`, `http:<name>`, and later `codex-cli`, `openai-api`. |
| Weight | An agent's model size: heavy, standard or light. Maps to Opus, Sonnet or Haiku on Claude links. |
| Chain | An agent's ordered list of links. Fixed in the agent's file, always ending in a free `http:` endpoint. |
| Backstop | The free open-source `http:` endpoint every chain ends in. Mandatory. |
| Cost class | `subscription`, `free` or `paid`, decided mechanically per link before each session. |
| Capacity profile | `{ctx, slots, cost_class, effort}` derived per link before each session; it sizes the brief and the pool take. |
| Brief | The prompt a worker receives: tools, role, repo context, standards, task. Assembled from 20 typed slots. |
| Slot | One named input to a brief, required or optional per agent. |
| Skill | A Markdown standards document embedded in the binary, preinjected by file globs or pulled on demand. |
| Lens | A reviewer rule catalog applied by file scope. Ten exist. |
| Finding | A reviewer claim with a rule ID, `file:line` and a quoted evidence line that Go verifies. |
| EARS | The acceptance-item grammar: `WHEN <event> THE SYSTEM SHALL <response>`, with `IF` and `WHILE` forms. |
| Check | The zero-token stage that reruns `done_when`, scope, secrets, impact tests, baseline diff and coverage. |
| Verify | The stage where the reviewer and the tester run at once on the same commit. |
| Pool slot | One of 1 to 6 machine-wide session slots that only builder, reviewer and tester sessions take. |
| Repo id | The normalized origin URL, or the git common dir path when there is no remote. Names worktrees, leases and caches. |
| Envelope | The JSON reply of every verb: `{v, ok, verdict, report, data, error}`. |
| Guard | The in-process, fail-closed check every tool call passes before it runs. |
| Sandbox | The OS-level confinement every `run` command executes in: seatbelt, bwrap or Docker. |
| Gap | A fact the plan has not verified or a decision not yet confirmed, tracked as `G-NN` in the gap register. |
| arc42 | The 12-section architecture documentation template this document's section set follows. |

---

## 3. Document metadata

### 3.1 Owners

| Role | Who | Responsibility |
|---|---|---|
| Document owner | Komodo domain architect (CEO) | Approves the architecture; decides every open gap |
| Author | Technical lead | Keeps the document true to the plan and the code |
| Reviewers | Domain architect, per component | Signs off component by component before phase 1 ends |

### 3.2 Current controls

| Control | Value |
|---|---|
| Document | High-Level Design (HLD) |
| System | Komodo 1.0.0-beta.6, standalone model-agnostic Go harness |
| Version | 0.1, draft |
| Status | Awaiting architecture sign-off by the document owner |
| Date | 2026-10-09 |
| Source of truth | `PLAN.md` at commit `ba4ff823` on `feat/1.0.0-beta.6` |
| Change control | Patches only, smallest span; a moved design decision adds a Review changes row in `PLAN.md` until phase 1 removes it |
| Approval control | No phase 2 code starts until the owner has signed off every component |
| Gap control | Every `[PENDING G-NN]` tag resolves to one row in the gap register (`docs/lld.md` §39); the gate fails a tag with no closing task |

### 3.3 Revision history

| Version | Date | Author | Change |
|---|---|---|---|
| 0.1 | 2026-10-09 | Technical lead | First draft from `PLAN.md`; enterprise section set; placeholders for open gaps |

### 3.4 Related documents

| Document | Relationship |
|---|---|
| `docs/lld.md` | The Low-Level Design that details every component here |
| `PLAN.md` | Interim source of truth; removed once phase 1 lands |
| `docs/prd.md` | Product requirements, phase 1 deliverable |
| `docs/decisions/0001-standalone-harness.md` | First architecture decision record, phase 1 deliverable |
| Appendix B | External references |

**Placeholder convention.** Text in the form `[PENDING G-NN]` marks a fact the plan has not yet verified or a decision the owner has not yet confirmed. No placeholder hides a proposal; each stands in for content that is decided but not yet proven.


---

## 4. Introduction and goals

### 4.1 Purpose

This document describes the architecture of Komodo beta 6 at the level a domain architect needs to approve it: what the system is for, who has a stake in it, what constrains it, what it is made of, how the parts cooperate at run time, what data it keeps, how it stays safe, and how it will be delivered. It follows the arc42 structure with the owner's ordering for the front matter.

### 4.2 Scope

In scope:
- The single Go binary `komodo`, its 27 components in 8 layers, and the cross-cutting security design.
- Two callers: an AI orchestrator (Claude Code today) and a human at a shell.
- Three model links that ship in beta 6: `claude-cli`, `anthropic-api`, and OpenAI-compatible `http:` endpoints (Ollama, vLLM, llama.cpp, LM Studio, local or on a server).
- Four platforms: macOS, Linux, WSL2, and native Windows (Docker required).

Out of scope, deferred to a later minor version:
- The OpenAI execution path: `codex-cli` link, `openai-api` chain link, `.codex/` install. Beta 6 ships the interfaces and probe only; the links refuse with `not yet`.
- `komodo chat`, Komodo's own orchestrator front end.
- `orch agent`, ad hoc delegation outside the pipeline.
- Repo-defined hooks and agents, keychain-backed secrets, OpenTelemetry export, Docker on macOS and Linux, service-backed test runs.

### 4.3 Business drivers

The prototype used Claude Code itself as the engine, and the effort went into fighting hooks, settings, plugins and render drift:

| Symptom | Measured |
|---|---|
| One task group needed repeated fixes | 11 fix commits on TG-15.4 |
| Stale renders and stale binaries | TG-15.3 |
| The guard failed open and blocked the builder's own work | 187 refusals of the builder's own commands |
| Retries | Effectively unbounded |
| Spend cap | Always $0 |

Every prior alpha and beta, including the first beta 6 attempt, is treated as a failed prototype. The concepts carry forward; the code starts fresh in place with the prototype parked in `backups/` and tagged `prototype-final`.

### 4.4 Requirements overview

| Requirement | Statement |
|---|---|
| Engine | One Go binary, standard library only, no server process |
| Workers | Stateless model sessions, one fresh session per task, each with a private toolset |
| Callers | An orchestrator and a human at a shell call the same CLI verbs; the orchestrator gets a JSON envelope |
| Pipeline | Five stages per task group: Intake, Build, Check, Verify, Ship |
| Isolation | Worktree plus guard plus OS sandbox for every worker |
| Models | A declared fallback chain per agent that always ends in a free open-source endpoint |
| Delivery | Fenced landings into an epic branch; a person merges only the epic pull request |
| Operations | Self-updating binary, one configuration file, no daemon, optional OS-scheduled tick |

### 4.5 Goals and non-goals

Goals:

| Goal | Measure |
|---|---|
| 80% mechanical, 20% prompt | Any failure a guard row, parser or check can prevent is prevented in Go |
| Bounded loss | Any single failure loses at most one model turn and no committed edit |
| Never pay by accident | 0 paid calls under `providers.paid: never`; a three-step billing check fails closed |
| Open source as the backstop | Every agent chain ends in a free `http:` endpoint; Claude limits move work there first |
| Every loop has a counter | Attempts, repairs, retries, refusals, pauses and budgets each have a bound and a named final state |
| Deterministic output | Models return codes and facts; Go templates render every word a person reads |

Non-goals for beta 6:

| Non-goal | Reason |
|---|---|
| A cost cap of Komodo's own | Provider windows and spend limits are the provider's; USD is a beta metric that only warns |
| Per-run or per-repo configuration | One machine file keeps settings auditable and out of any agent's reach |
| Repo-defined agents, hooks or plugins | Deferred; the embedded roster and three inline hooks are enough for beta 6 |
| Third-party Go dependencies | Standard library only, by locked decision |
| A long-running service | Every verb is a short process; the engine runs detached per `run` and a tick replaces a daemon |

### 4.6 Quality goals

The five quality goals that shape the architecture, in priority order.

| Priority | Quality goal | Scenario |
|---|---|---|
| 1 | Safety | A worker can never write outside its worktree, read a secret, push to git, or spend money without a decided setting |
| 2 | Durability | A crash, kill, power loss or provider halt mid-run loses at most one model turn and no committed edit |
| 3 | Determinism | The same facts render the same bytes on every run; a stage moves only on evidence the engine produced |
| 4 | Cost control | Work prefers the subscription and free links, caches a stable prompt prefix, and bounds every loop |
| 5 | Portability | The same behaviour on macOS, Linux, WSL2 and native Windows from one binary |

### 4.7 Stakeholders and concerns

| Stakeholder | Role | Concerns |
|---|---|---|
| Domain architect (owner) | Approves the architecture, decides gaps, cuts releases | Correctness of the design, cost, safety, enterprise upgrade paths |
| Human operator | Installs, configures, updates, merges epic PRs | Install is idempotent; updates never break a live run; `status` explains every block |
| Orchestrator (Claude Code) | Plans, runs, waits, answers scope requests | One verb set with self-describing help; a verbatim `report`; wakes instead of polling |
| Worker agents | Build, review, test, plan, research | A brief that fits; tools that teach on refusal; no harness rules in the prompt |
| Repository owners | Own the code Komodo changes | Nothing committed outside the backlog's scope; `AGENTS.md` stays the source of truth; no hooks or config added |
| Model providers | Anthropic, local endpoints | Rate limits and spend limits honoured; retries jittered; subscription login never bypassed |
| Future maintainers | Build phases 2 to 9 | One package per layer; import direction enforced; every generated artifact gated against its source |

---

## 5. Architecture constraints

### 5.1 Technical constraints

| Constraint | Statement |
|---|---|
| Language and dependencies | Go, standard library only; `go.mod` has no dependencies |
| Process model | No server process; every verb is a short process; the engine runs detached per `run` |
| Platforms | macOS, Linux, WSL2 (preferred on Windows), native Windows `.exe` which requires Docker |
| Host bridge | Claude Code talks to the harness only through CLI subcommands with `--json` |
| Model access | Claude through `claude -p` on the subscription login, the Anthropic API when allowed, and OpenAI-compatible HTTP; OpenAI's own path is a later minor |
| Isolation | Worktree plus guard plus OS sandbox; each session gets a private MCP tool server; native host tools are off |
| Configuration | One machine file `~/.komodo/config.json`, changed only through `komodo config`; no repo layer, no per-run override; runtime state outside the tree |
| Sandbox backends | seatbelt on macOS, bwrap on Linux and WSL, Docker on native Windows only |

### 5.2 Organizational constraints

| Constraint | Statement |
|---|---|
| Ownership | One owner approves the architecture and holds the release signing key |
| Approval gate | No phase 2 code starts until every component is signed off |
| Build model | Phases 2 to 6 by hand in plain Claude Code sessions; from phase 7 beta 6 builds its own groups |
| Security review | The security lens is required for the installer, binary lifecycle, HTTP stack, tool runtime, guardrails, sandbox and ship; before phase 7 the owner reviews those groups by hand |
| Prototype | Beta 5 retires before phase 2; its code stays in `backups/` as reference only, never as a source of rules |

### 5.3 Conventions

| Convention | Statement |
|---|---|
| Documentation | `AGENTS.md` is the source of truth in every repo; `CLAUDE.md` imports it; specs restart from zero |
| Carry-over | Backlog tree, lint, release, worktree protections, subagent ledger, economy mode (now `mode`), all 34 `standards-*` skills |
| Deleted | Eval, recall, plugins, standalone facets |
| Output | Every word a person reads comes from a Go template selected by `output.profile` (`adhd` by default) |
| Events | A new event type or result code is a plan change: enum, schema, template and fixture together |
| Gaps | A design section may not say "later" or "TBD" without a `G-NN` register row |

---

## 6. Context and scope

### 6.1 Business context

```mermaid
flowchart LR
  H["Human at a shell"] -->|komodo verb| K
  O["Orchestrator: Claude Code session"] -->|komodo verb --json| K
  K["komodo binary (Go, stdlib only)"]
  K -->|claude -p + private MCP| C["Claude CLI (Max login)"]
  K -->|HTTPS, Go tool loop| A["Anthropic API"]
  K -->|HTTP, Go tool loop| L["OpenAI-compatible endpoints: Ollama, vLLM, llama.cpp, LM Studio"]
  K -->|git, gh| G["Git remote and GitHub PRs"]
  K -->|latest.json, binaries| R["GitHub Releases"]
  K -->|sandbox-exec, bwrap, docker| S["OS sandbox"]
  K -->|launchd, cron, Task Scheduler| T["OS scheduler (opt-in)"]
```

| Actor or system | Role | Direction |
|---|---|---|
| Human at a shell | Installs, configures, updates, releases; may also run every shared verb | Calls in |
| Orchestrator | Refines intent, calls `plan`, `run`, `status --wait`, answers `scope`, relays reports | Calls in |
| Claude CLI | Runs worker sessions on the Max subscription via `claude -p` | Called out |
| Anthropic API | Runs worker sessions when a paid link is allowed | Called out |
| OpenAI-compatible endpoints | Run worker sessions as the free backstop, local or self-hosted | Called out |
| Git remote and GitHub | Receives group and epic branches; `gh` opens PRs; a person merges only the epic PR | Called out |
| GitHub Releases | Serves `latest.json` and signed binaries for self-update | Called out |
| OS sandbox | Confines every `run` command | Called out |
| OS scheduler | Fires `komodo jobs tick` every 5 minutes when installed with `--schedule` | Calls in |

### 6.2 Technical context

| Interface | Protocol | Direction | Notes |
|---|---|---|---|
| CLI verbs | Process invocation; JSON envelope on stdout | Inbound | Exit codes 0 to 4; `--json` on automatically without a terminal |
| `claude -p` | Child process; stream-json on stdin and stdout | Outbound | Private MCP tool server over stdio per session |
| Anthropic Messages API | HTTPS, SSE streaming | Outbound | Structured outputs for the final call; `cache_control` breakpoints |
| OpenAI-compatible `/v1` | HTTP or HTTPS, SSE streaming | Outbound | `GET /models` probe; Ollama detected via `GET /api/version`; `/api/ps` for context |
| Git | Child process | Outbound | Worktrees, hidden refs by compare-and-swap, branches, merges, pushes |
| GitHub | `gh` child process; release download over HTTPS | Outbound | Group and epic PRs; `latest.json` and binaries |
| OS sandbox | `sandbox-exec`, `bwrap`, `docker exec` | Outbound | Wraps every `run`; never the model client |
| OS scheduler | launchd, cron, Task Scheduler | Inbound | Runs `komodo jobs tick` every 5 minutes |

### 6.3 Inputs and outputs

| Input | Form | Consumed by |
|---|---|---|
| A goal | `komodo plan <goal>` text, or `--from <spec>` documents (PRD, RFC, ticket export, `docs/` tree) | Planner, two passes |
| The repository | Markers (`go.mod`, `package.json`, …), `AGENTS.md`, the symbol graph | Detection, brief builder, repo map |
| The backlog | `docs/backlog/epic-N/tg-N.M/{TG.md, tsk-*.md}` | Intake, Build, Verify |
| Configuration | `~/.komodo/config.json` within built-in hard limits | Every verb, snapshotted per run |
| Provider state | `providers.json` from the install probe; `quota.json` reset timers | Chain selection, billing check |
| Decisions | `scope approve|deny`, `resume`, `abandon`, config writes | The engine |

| Output | Form | Produced by |
|---|---|---|
| A linted backlog | Files under `docs/backlog/`, written only if lint passes | Planner pass plus Go lint |
| Group commits | On the hidden ref, then a group branch `komodo/<epic>/<group>` | Build, Ship |
| Group PRs | Into the epic branch `komodo/<epic>`, merged by the engine (`ship.mode: group`) | Ship |
| The epic PR | Into the base branch, opened ready for a person to merge | Epic gate |
| Reports | The envelope's `report` text, `status`, the run report, `status --metrics` | Templates |
| Ledgers | `sessions.jsonl`, `events.jsonl`, `tasks.json`, `groups.json` | Observability subscriber |
| Signed releases | Tag, cross-built binaries, checksums, ed25519 signature, changelog | `komodo release` |

---

## 7. Solution strategy

### 7.1 Key decisions

| Quality goal | Strategy |
|---|---|
| Safety | A minimal tool surface with no shell; an in-process fail-closed guard on every call; an OS sandbox around every command; only the engine writes git |
| Durability | Journal-first events, one pure decider, journaled actions with idempotency keys, a WIP commit at every session end, stage epochs that fence late events |
| Determinism | Typed result schemas enforced twice; codes and facts rendered by Go templates with golden fixtures; the engine, never a model, emits events |
| Cost control | A fixed chain per agent ending in a free endpoint; a mechanical billing check; a stable five-part prompt prefix with four cache breakpoints; bounds on every loop |
| Portability | One binary cross-built at every gate; one sandbox backend per OS behind one interface; per-platform copy mechanics behind one "copy" verb |

### 7.2 Technology choices

| Choice | Decision |
|---|---|
| Language | Go, standard library only |
| Model transports | `claude -p` with a private stdio MCP server; a Go tool loop for HTTP links |
| State | Append-only JSONL journals plus atomic snapshots; create-only files for machine-wide locks |
| Concurrency | One goroutine per group owning its state; cross-group state in files with OS atomicity |
| Caching | Content-addressed store verified on read; provider prompt caching by prefix order |
| Rendering | Go `text/template` with a six-function map and golden fixtures |

### 7.3 Precedents

| Idea | Precedent |
|---|---|
| Pure decider, journaled side effects, replay | Temporal's workflow and activity split |
| Fencing tokens on leases, enforced by the resource | Distributed-lease practice |
| Event stream as the agent's spine | OpenHands |
| Minimal tool surface, no shell; sandbox with network off | SWE-agent's agent-computer interface; Codex CLI |
| Seatbelt on macOS, bwrap on Linux | Codex CLI |
| Full-jitter retries | AWS Architecture Blog |
| Ranked repo map for planning | Aider |
| EARS acceptance items | Kiro specs |
| Coverage bands 60/75/90 | Google Testing Blog |
| Headless sessions, resume, structured output | Claude Code 2.1.295 |

---

## 8. Building block view

### 8.1 Level 1: layers

Eight layers, 27 components, 11 Go packages. Each package holds the components of one layer; no package maps to a single component.

| Layer | Purpose | Packages |
|---|---|---|
| Interface | How a person or orchestrator reaches the harness and how it reaches the machine | `cli`, `host`, `core` |
| Model I/O | Reaching models and giving them tools | `provider`, `tools`, `agent`, `core` |
| Context | What a model reads: skills, role text, the brief | `agent` |
| Safety | What a model may do: guard and sandbox | `safety` |
| Orchestration | Driving groups through stages | `engine` |
| Quality | Proving the work: Check, review, tester | `quality` |
| Economy and ops | Budgets, cache, ledger, events | `ops`, `core` |
| Delivery | Backlog, shipping, the gate | `repo`, `safety` |

### 8.2 Level 2: components

| Layer | Component | Responsibility |
|---|---|---|
| Interface | CLI processor | One verb set, one envelope, five exit codes, pin check, five-step pipeline |
| Interface | Installer | Puts the binary on the machine and the Komodo block into a repo, idempotently, from a manifest |
| Interface | Binary lifecycle | Hourly release check, background download, verified hot swap, runs pinned to their version |
| Interface | Configuration | 33 flat keys plus agent and endpoint groups, hard limits, hash guard, audit log, command detection |
| Model I/O | Provider adapters | One interface over every link; chain and failover; billing check; measured context; model catalog |
| Model I/O | HTTP stack | One client: TLS roots, proxy, SSE streaming, jittered retries, limit classes, host allowlists, redaction |
| Model I/O | Agent registry | Seven agents decoded once from `agent.json` and `prompt.md`; cross-checked at the gate |
| Model I/O | Tool runtime | Tool registry, private MCP server, harness tool loop for API links, output caps, context compaction |
| Context | Skill loader | Indexed, hashed, size-bounded standards skills; preinject by glob, pull on demand |
| Context | Rules and output contract | Orchestrator rules embedded; codes and facts rendered by Go templates per output profile |
| Context | Brief builder | Stable five-part prefix, 20 slots, one ceiling per link at half its measured context |
| Safety | Guardrails | Fail-closed in-process check on every tool call; rows are `policy.json` data with scopes |
| Safety | Sandbox and process control | Seatbelt, bwrap or Docker around every `run`; process, memory, time and output caps; tree kill |
| Orchestration | Orchestration engine | Pure `Next()`, `Drive` per group, five stages, DAG, queue, pool, convergence |
| Orchestration | Workspace and leases | Worktree per group, lease with fence, hidden ref, shutdown, parent guard, sweep |
| Orchestration | Session supervisor | Spawn, heartbeat, time warning, nudge, stop ladder, exit reasons |
| Orchestration | Scheduler and job templates | Job templates, idempotent `jobs tick`, counters, overlap through the lease |
| Orchestration | Error recovery | One row per failure: detector, action, bound, final state; outbox keys; startup sweep |
| Quality | Check | Zero-token ground truth: `done_when`, scope, secrets, baseline, impact set, flake rule, coverage |
| Quality | Review and evidence | Ten scoped lenses, verified findings, per-task verdicts, blind tester, precision ledger |
| Economy and ops | Token and resource management | Budgets, prompt-cache breakpoints, headline cost metrics |
| Economy and ops | Cache store | Content-addressed, verified-on-read, LRU-bounded store outside every worktree |
| Economy and ops | Runtime observability | Session ledger, event log, lock-table views, scorecard, live `status` |
| Economy and ops | Event bus | Envelope, catalog, journal, in-order delivery, three read-only subscribers, wakes |
| Delivery | Backlog and planning | Backlog grammar, lint, two-pass planner, Go-derived DAG, repo map, EARS items |
| Delivery | Ship | Only writer of git: fenced landings, group and epic PRs, signed releases |
| Delivery | Gate and self-checks | Build, structure, safety, events, docs drift, generated artifacts, timing; Komodo repo only |

### 8.3 Component diagram

```mermaid
flowchart TB
  subgraph Interface
    CLI[CLI processor] --- INS[Installer] --- FRESH[Binary lifecycle] --- CFG[Configuration]
  end
  subgraph Orchestration
    ENG[Orchestration engine] --- WS[Workspace and leases] --- SUP[Session supervisor] --- SCH[Scheduler] --- REC[Error recovery]
  end
  subgraph Quality
    CHK[Check] --- REV[Review and tester]
  end
  subgraph Context
    BRIEF[Brief builder] --- SK[Skills] --- RULES[Rules and output]
  end
  subgraph ModelIO["Model I/O"]
    PROV[Provider adapters] --- HTTP[HTTP stack] --- AG[Agent registry] --- TOOLS[Tool runtime]
  end
  subgraph Safety
    GUARD[Guardrails] --- SBX[Sandbox]
  end
  subgraph Ops["Economy and ops"]
    BUS[Event bus] --- LED[Observability] --- CACHE[Cache store] --- BUD[Budgets]
  end
  subgraph Delivery
    PLAN[Backlog and planning] --- SHIP[Ship] --- GATE[Gate]
  end
  CLI --> ENG
  ENG --> SUP --> PROV
  ENG --> CHK --> REV
  ENG --> SHIP
  SUP --> BRIEF --> SK
  BRIEF --> AG
  TOOLS --> GUARD --> SBX
  PROV --> TOOLS
  ENG --> BUS --> LED
  ENG --> WS
  PLAN --> ENG
```

### 8.4 Dependency rules

Imports flow one way: `cli` → `engine` → `quality` → `host` → `repo` → `provider` → `tools` → `ops` → `agent` → `safety` → `core`. A package may skip levels. Higher-level needs enter lower packages as parameters: the engine passes a session runner into `quality`, Check into `repo`, backlog lint into `tools`, and filled slots into `agent`. `komodo gate` refuses any import that points up.

### 8.5 Concepts to components

| Concept | Lives in |
|---|---|
| Agents | Agent registry: `agent.json` for what Go reads, `prompt.md` for what the model reads |
| Skills | Skill loader: indexed, preinjected or pulled, usable by external sessions |
| Rules | Rules and output contract: embedded in the binary; the output style is config |
| Hooks | Event bus: Go lifecycle events. Exactly three inline hooks exist; repo hooks wait for a later minor |
| Guards | Guardrails: checked in-process on every tool call, fail-closed |
| Commands | Configuration detects command classes; the tool runtime runs them; only Ship runs git writes |
| Plugins | Deleted. External MCPs become an opt-in allowlist per agent |

---

## 9. Runtime view

### 9.1 Scenario: the event spine

Facts flow as events; one pure function decides. The mechanical parts publish what happened; `Next(state, event)` returns the new state, the actions to take and any follow-on events. Nothing else changes group state.

```mermaid
flowchart LR
  P["Producers: workspace, supervisor, Check, review, ship, timers"] -->|fact| J[("group journal: events.jsonl")]
  J --> N["Next(state, event)"]
  N -->|new state| S[("state.json snapshot")]
  N -->|follow-on events| J
  N -->|actions| A["Engine actions: spawn, kill, commit, land"]
  A --> P
  J --> R["Read-only subscribers: ledger, status, status --wait"]
```

| Principle | Rule |
|---|---|
| The engine emits, never a model | A session's typed JSON becomes an event only after Go validates it. A killed session cannot report; a model can forget or invent. |
| Journal first | Each event is appended and fsynced to `runs/<group>/events.jsonl` before it is applied. `state.json` is a snapshot with the last `seq`; resume replays after it. |
| Subscribers are read-only | The ledger, `status --wait` wakes and `status --watch` react to events and never change state. |
| Coarse grain | Events fire per run, group, stage, session and task, never per model turn or token. |
| In-process and ordered | One bus per engine, synchronous, in `seq` order, no broker. Machine-wide state stays in files; each engine turns a change there into a local event. |
| Outside the bus | The guard, formatting after `edit`, and shutdown are direct code. They log an event afterward when they can. |
| Actions are journaled (the outbox) | `Drive` appends `action.start {id, kind}` before an action and `action.done` after. On replay a done action is skipped, a started one reruns, and every action is idempotent by a declared key. |
| One journal per group, one per run | Groups never read each other's journals; they meet only in the queue and the pool. |
| Epochs fence late events | Every spawn carries the stage's epoch. A session event with an older epoch is journaled `stale: true` and ignored. |
| A new type is a plan change | The catalog is a Go enum; the gate fails on an emitted type missing from it, or a listed type nothing emits. |

Guarantees the tests pin:

| Guarantee | How |
|---|---|
| Ordered, gap-free | One writer per journal; `seq` checked on append and replay |
| Durable before applied | Append, fsync, then `Next()`; a crash after the sync replays the event |
| Exactly-once state moves | A stage moves only in `Next()`, keyed by `seq`; stale-epoch events never move it |
| At-least-once actions, safe to repeat | Every action has an idempotency key |
| No lost wake | `status --wait` reads the journal tail after subscribing |
| Subscriber faults contained | A panicking subscriber is logged and dropped; state is untouched |

### 9.2 Scenario: a group's lifecycle

Five stages plus two holding states. Sessions run in Build and Verify, so Paused and Blocked can follow either.

```mermaid
stateDiagram-v2
  [*] --> Intake: run.start
  Intake --> Build: stage.exit
  Build --> Check: task.done (last task)
  Check --> Verify: check.done pass
  Check --> Build: check.done fail
  Verify --> Build: finding.verified
  Verify --> Ship: stage.exit
  Ship --> [*]: group.shipped
  Build --> Paused: session.end provider_limit
  Paused --> Build: timer.due
  Paused --> Blocked: pause_timeout
  Build --> Blocked: a bound is hit
  Verify --> Blocked: a bound is hit
  Blocked --> Build: resume
```

| Stage | What happens | Tokens | Exit needs |
|---|---|---|---|
| Intake | Lint the group, hash its card, take the lease and fence, cut the worktree from the epic tip, run `commands.setup` with network on | 0 | lint pass, lease and fence taken, setup exit 0, task list locked |
| Build | One fresh builder session per task, in sequence, in the group worktree. After each task Go reruns its `done_when` and commits to the hidden ref | model | `task.done` for every task, each with `done_when` exit 0 and a commit |
| Check | Rerun every `done_when`, scope check, secret scan, impact-set tests, baseline diff, changed-line coverage | 0 | `check.done pass` |
| Verify | Reviewer (ten lenses, per-task verdicts) and tester (blind acceptance items) run at once on the same commit, the tester in its own throwaway worktree | model | reviewer `met` for every task and 0 blocking findings; tester `confirmed` for every task and item; 0 regressions |
| Ship | Merge the epic branch into the group branch, rerun Check, land with the fence, then Check on the epic tip | 0 | merge clean, Check passes, landing succeeds |
| Paused | A provider limit with no link left: the group returns its pool slot, keeps lease and worktree, waits on a disk timer. Wall clocks stop | 0 | the timer fires, or the pause cap ends it |
| Blocked | A bound was hit or a decision is needed. A person or the orchestrator runs `resume` | 0 | `resume`, `scope approve|deny`, or `abandon` |

Back to Build: a failed Check or Verify resumes the task's own builder session with only the verified findings. There is no separate Repair stage. Each return raises the task's attempt count and the stage epoch. `limits.repairs` (3) returns per group, then Blocked.

### 9.3 Scenario: the Verify race

The reviewer and tester start under one epoch. The first verified failure makes `Next()` emit `cancel` for the other and enter Build under a new epoch. Whatever the cancelled session sends afterward carries the old epoch and is stale.

```mermaid
sequenceDiagram
  participant R as Reviewer (epoch 4)
  participant T as Tester (epoch 4)
  participant N as Next()
  T->>N: finding: item 3 failed, Go reran x2, still fails
  N->>N: Build, epoch 5; action cancel(R)
  R->>N: session.end done (epoch 4)
  N-->>R: journaled stale=true, ignored
```

| Case | Outcome |
|---|---|
| Reviewer fails first | Tester cancelled; back to Build with the reviewer's verified findings |
| Tester fails an item and Go's two reruns also fail | Reviewer cancelled; back to Build with the item |
| Tester fails an item but Go's two reruns pass | Tester restarts once with Go's output; reviewer keeps going |
| Go's two reruns split | Group blocks with `flaky` |
| Both pass | Both `session.end done` events needed; then `stage.exit` to Ship |
| Both fail in one tick | Handled in `seq` order; the second is already stale |
| An item has no command | It never cancels the reviewer |

### 9.4 Scenario: run order across groups

```mermaid
flowchart LR
  BL["Backlog: groups with touches and dependencies"] --> DAG["Go builds the DAG, refuses cycles, computes waves"]
  DAG --> Q["Machine-wide work queue (files)"]
  Q --> POOL["Session pool: 1 to 6 slots, plus per-link slots"]
  POOL --> G1["Group A: Intake to Ship"]
  POOL --> G2["Group B: Intake to Ship"]
  G1 --> LAND["One landing at a time, then Check on the epic tip"]
  G2 --> LAND
  LAND --> EG["Epic gate: reviewer and tester over the whole epic diff"]
  EG --> PR["Epic PR, ready for a person"]
```

| Rule | Detail |
|---|---|
| A graph, not a list | Groups form a DAG from the backlog. A group with two dependencies joins after both land. |
| Start rule | A group starts only after every dependency has landed and Check has passed on the epic tip. |
| One queue | Ready work from every repo and every job waits in one machine-wide queue of create-only files. |
| Pick order | `priority: high` first, then fair share across repos, then the longest remaining chain weighted by task count, then most groups unblocked, then backlog ID order. |
| No waiting on siblings | A group goes from Build to Verify as soon as its own builder finishes. |
| Integration break | If Check fails on the epic tip after a landing, landings pause and the last-landed group goes back to Build in a new worktree cut from the tip. |
| Epic gate | After the last group lands, the reviewer and tester run once across the whole epic diff. A finding goes to the group that last changed its file; a finding with no file blocks the epic with `epic_gate`. |
| Abandoned dependency | Its dependents block with `dependency`, naming it. |

### 9.5 Scenario: the session pool

| Rule | Detail |
|---|---|
| Who takes a slot | Builder, reviewer and tester sessions only, in runs and jobs alike. Planner and job sessions take none but still count against their link's limit. |
| Floor and ceiling | `limits.pool_min` (1) stays open while throttled; `limits.pool_max` (6) is the most at once; hard limit 16. |
| Per link | `limits.link_sessions.<link>` (3; loopback endpoints 1) across all repos. |
| Adapts | Grows by 1 after each clean session end while free memory covers one more `sandbox.max_mem_gb`; halves, never below the floor, on a rate-limit error or quota past `warn.quota_pct`. |
| Machine-wide | Pool and link slots are lock files under `~/.komodo/state/`, freed when their pid dies. Two engines coordinate only through files. |

### 9.6 Scenario: one session, end to end

```mermaid
sequenceDiagram
  participant E as Engine (Drive)
  participant B as Brief builder
  participant P as Provider adapter
  participant M as Model session
  participant T as Tool runtime + guard + sandbox
  participant C as Check
  E->>P: capacity profile for the link (ctx, slots, cost_class, effort)
  E->>B: fill 20 slots; fit under half of ctx
  E->>P: billing check (fail closed), spawn with brief
  P->>M: claude -p with private MCP, or Go tool loop
  loop until a text-only reply
    M->>T: tool call
    T->>T: guard (fail-closed), sandbox for run
    T-->>M: capped result
  end
  M->>P: final call: result schema only
  P->>E: Go re-validates the result (1 re-prompt)
  E->>C: rerun done_when
  E->>E: commit to hidden ref with fence; journal task.done
```

| Step | Rule |
|---|---|
| Capacity profile | Context is measured, not typed: Models API `max_input_tokens`, Ollama `/api/ps`, or the endpoint's declared `ctx`. |
| Brief | Order is tools, role, repo context, standards, task. The brief must fit in half the measured context; over that, the next link in the chain that fits; none left, Blocked `context`. |
| Billing | `claude auth status --json` sets the cost class; the env is scrubbed of API keys; the first stream line must say `apiKeySource: "none"` or the session is killed before turn 1. |
| Tools | Only the agent's listed tools are advertised. Every call passes the guard; `run` executes in the sandbox with no shell and no network. |
| Result | The provider enforces the schema, then Go re-validates. One re-prompt, then the task fails. The agent's own gate (`done_when`, `lint_backlog`) runs next; two rejected results end the session with `check_fail`. |
| Commit | Every writing session ends in a WIP commit to the hidden ref by compare-and-swap with the fence. |

### 9.7 Scenario: from goal to merged epic PR

1. The human or orchestrator runs `komodo plan <goal>`. The planner's heavy pass drafts groups, goals, `touches` and dependencies; Go lints. A standard-weight pass per group drafts tasks and EARS items in parallel; Go lints each. Files are written only when lint passes.
2. `komodo run` builds the DAG, enqueues wave-1 groups, returns the run id at once, and the engine continues detached.
3. The orchestrator runs `komodo status --wait` in the background and is woken on `group.blocked`, `group.paused`, `run.done` or `run.stopped`.
4. Each group moves Intake → Build → Check → Verify → Ship. A builder that needs a file outside its `files` list stops with `needs_scope`; the engine denies protected paths and paths another live group owns, and otherwise the orchestrator answers `scope approve|deny`.
5. Each landing is fenced. After each, Check runs on the epic tip. Dependents start as their dependencies land.
6. After the last group, the epic gate runs. The engine opens the epic PR into the base branch, ready for review.
7. A person merges the epic PR; `komodo sync` fetches, prunes worktrees and refreshes repo files.

---

## 10. Data view

### 10.1 Where data lives

| Location | Contents | Committed |
|---|---|---|
| `<repo>/AGENTS.md` | Repo rules plus the Komodo block (`komodo:begin min=<version>` … `komodo:end`) | Yes |
| `<repo>/CLAUDE.md` | `@AGENTS.md` import line | Yes |
| `<repo>/.claude/settings.json` | Two entries only: the `Bash(komodo *)` allow rule and a `SessionStart` hook running `komodo status` | Yes |
| `<repo>/docs/backlog/epic-N/tg-N.M/` | `TG.md` and `tsk-*.md`, the backlog grammar | Yes |
| `<git-common-dir>/komodo/` | Install manifest, run and group journals, snapshots, job state, session files, ledgers, cache | No |
| `~/.komodo/config.json` | The one config file, created on the first `komodo config` write | No |
| `~/.komodo/bin/`, `versions/` | Current binary copy and every installed version | No |
| `~/.komodo/jobs/` | Job templates | No |
| `~/.komodo/leases/<repo-id>/` | One lease and fence file per group, machine-wide | No |
| `~/.komodo/wt/<repo-id>/` | Detached worktrees per group | No |
| `~/.komodo/state/` | Provider probe, quota timers, pool and link slots, queue, wake files, fresh state, config hash and audit log | No |
| `~/.komodo/cache/lang/<repo-id>/` | Komodo's own language caches per repo, never the user's | No |

On Windows, `~/.komodo` is `%LOCALAPPDATA%\komodo`.

### 10.2 Data flow

```mermaid
flowchart TB
  GOAL["Goal or spec docs"] --> PLANNER["Planner (2 passes) + Go lint"]
  REPO["Repo: markers, AGENTS.md, symbols"] --> DETECT["Detection: command classes, repo profile"]
  REPO --> MAP["Repo map (ranked symbol graph)"]
  MAP --> PLANNER
  PLANNER --> BACKLOG["docs/backlog/*"]
  BACKLOG --> INTAKE["Intake: lock the task list (card hash), lease, worktree, setup"]
  DETECT --> INTAKE
  CONFIG["~/.komodo/config.json (snapshot hash per run)"] --> INTAKE
  INTAKE --> BUILD["Build: brief per task; commits on the hidden ref"]
  SKILLS["Embedded skills, lenses, rules, templates"] --> BUILD
  BUILD --> CHECK["Check: done_when, scope, secrets, baseline, impact, coverage"]
  CHECK --> VERIFY["Verify: reviewer findings, tester evidence"]
  VERIFY --> SHIP["Ship: group branch, group PR, fenced landing"]
  SHIP --> EPIC["Epic branch, epic PR"]
  BUILD --> JOURNAL[("events.jsonl + state.json")]
  CHECK --> JOURNAL
  VERIFY --> JOURNAL
  SHIP --> JOURNAL
  JOURNAL --> LEDGER[("ledger: sessions, events, tasks, groups")]
  LEDGER --> STATUS["status, status --wait, status --metrics, run report"]
  CACHE[("cache: skills, context, checks, review, setup, map")] <--> BUILD
  CACHE <--> CHECK
  CACHE <--> VERIFY
```

### 10.3 Configuration model

| Layer | Where | Power |
|---|---|---|
| 1 Built-in | `embed/defaults.json`, `embed/policy.json` inside the signed binary | Defaults and hard limits |
| 2 Machine | `~/.komodo/config.json`, top level | Any value within the hard limits |

- The machine value wins within the hard limits. Every key exists out of the box; `--default` resets.
- One write path: `komodo config`, which needs a terminal, writes every pair or none, logs one audit row per changed key, and stores a SHA-256 of the file. A hand edit makes every verb refuse with `config_tampered` until `komodo config --accept`.
- Each run snapshots the effective config's hash into its `state.json`; edits mid-run do nothing to that run.
- Only two environment variables exist: `KOMODO_HOME` and `NO_COLOR`. Secrets are `env:` references only.
- Security rows are never config. The only guard tunable is `guard.refusal_limit`.

### 10.4 Cache

| Property | Rule |
|---|---|
| Keys | `sha256(inputs + binary version)`; only the hourly release check uses time |
| Location | `<git-common-dir>/komodo/cache/`, outside every worktree, so an agent cannot reach or poison it |
| Integrity | Verified on read; a mismatch deletes the entry |
| Bound | `cache.max_mb` (256) per repo, least recently used out first |
| Never cached | Build responses, because they are nondeterministic and have side effects |
| Kinds | skills, context (file slices, trims, diffs, briefs), checks, review findings, setup results, repo map and outline lint, test baseline |

### 10.5 Observability data

| Store | Contents |
|---|---|
| `ledger/sessions.jsonl` | One row per session: id, agent, task, link, model, measured context, capacity profile, tokens in/out/cached, USD, wall time, skill tokens, exit reason; `wait_until` and `wait_link` when paused |
| `ledger/events.jsonl` | A read-only copy of every group journal's events, plus fallbacks, skips and host subagent events |
| `ledger/tasks.json`, `ledger/groups.json` | Lock-table views rebuilt from the journals: who holds what |
| `sessions/<id>/` | `brief.md`, `request.json`, `stream.jsonl`, `messages.jsonl`, `result.json`, `out/` |
| `status --metrics` | Per agent and model over the retention window: first-pass rate, repairs per group, finding precision, tester false-fail rate, tokens and USD per accepted group, cache hit rate |

Retention is `ledger.retention_days` (30) or 100 MB per repo, oldest first; a journal of an open, paused or blocked group is never trimmed. Keys and secret matches are masked before anything is written. There is no OpenTelemetry export in beta 6; JSONL plus `events.schema.json` is the export.

---

## 11. Model and provider view

### 11.1 Links

| Link | Runs as | Auth | Tools via | Beta 6 |
|---|---|---|---|---|
| `claude-cli` | `claude -p` at the probed absolute path | The CLI's own login | Private MCP server | Ships |
| `anthropic-api` | HTTPS through the HTTP stack | `providers.keys.anthropic` | Go tool loop | Ships |
| `http:<name>` | OpenAI-compatible HTTP: Ollama, vLLM, llama.cpp, LM Studio | Optional `key` per endpoint | Go tool loop | Ships |
| `codex-cli` | `codex exec` | The CLI's own login | Private MCP server | `not yet` |
| `openai-api` | HTTPS | `providers.keys.openai` | Go tool loop | `not yet` |

### 11.2 Weights and models

| Weight | Agents | Claude latest → prior | Effort (API links) |
|---|---|---|---|
| Heavy | Planner, architect, reviewer | `claude-opus-5-5` → `claude-opus-5` | `high` |
| Standard | Builder, researcher | `claude-sonnet-5-5` → `claude-sonnet-5` | `medium` |
| Light | Tester, scout | `claude-haiku-5-5` → `claude-haiku-4-5` | `low` |

- Every current Claude ID is a 1M-token window with 128K max output; Haiku 4.5 is 200K. IDs are exact and pinned, never aliases.
- `fable` (Fable 5.1) is an opt-in heavy model by `komodo config agent <name> model=fable`, only when the probe lists it for the login. `[PENDING G-06]` whether a Max login exposes it to `claude -p`.
- Local recommended models per weight: Qwen3.6-27B, Gemma 4 31B, Nemotron 3 Nano. Their windows are measured, never typed.
- The default window for an unmeasured, uncatalogued model is 262,144.

### 11.3 The chain and failover

```mermaid
flowchart LR
  A["1. claude-cli: latest ID"] -->|unavailable, unauthorized or over limit| B["1b. claude-cli: prior ID"]
  B --> C["2. Backstop: each free http: endpoint in config order"]
  C -->|only if providers.paid allows| D["3. anthropic-api"]
  C -->|nothing left| P["Pause on a disk timer until the provider's reset time"]
```

| Rule | Detail |
|---|---|
| Skip only on provider failure | A link is skipped only when unavailable, unauthorized or over its limit, never because a task failed. Each skip is a `provider.skip` event and a warning naming the model the task ran on. |
| The backstop is mandatory | Every chain must end in at least one free `http:` endpoint. `doctor` and `gate` refuse a chain without one. |
| Chains are fixed | In `agent.json`, never in config. `komodo config agent <name> model=…` picks the starting model; the rest of the chain follows in default order. An agent set to `-prev` falls back to the backstop, never forward. |
| Back to Claude | After the recorded reset time, new sessions start on `claude-cli` again; a task already on the backstop finishes there. |
| A model retired mid-run | `providers.json` is the only registry. A 404 fails that session start; the task's next session takes the prior link, then the backstop. |
| Context floor | `providers.ctx_floor` (262,144) applies to `http:` endpoints only. An endpoint below it fails `doctor` and is skipped with `ctx_below_floor`. Briefs are never shrunk to fit. |

### 11.4 Cost classes and the billing check

| Cost class | When | Runs? |
|---|---|---|
| `subscription` | `claude-cli` whose `auth status` says `authMethod: claude.ai` and a `pro`, `max`, `team` or `enterprise` plan | Always |
| `free` | An `http:` endpoint on loopback, or one marked `self_hosted: true` | Always |
| `paid` | `anthropic-api`, `openai-api`, `claude-cli` on any other login, or any other `http:` endpoint | Only as `providers.paid` allows: `never` (default), `fallback`, `allowed` |

Three mechanical checks, failing closed: the login is read before every `claude-cli` session; the worker environment is scrubbed of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL` and `CLAUDE_CODE_USE_*`; and the first stream line must report `apiKeySource: "none"` or the session dies before its first turn with exit `billing`. `[PENDING G-02]` capturing that init line at probe time without spending a model turn.

### 11.5 Limits, pauses and warnings

Komodo adds no cost cap of its own; provider windows and spend limits are the provider's. USD is a beta metric. On a quota stop the next session starts on the next allowed link; only with no link left does the session pause with `wait_until` (reset time plus 1 minute) on disk and the link's reset time in machine-wide `quota.json`. `komodo resume` attempts every paused agent now; a refused attempt costs no model turn and re-pauses with the new reset time, or backs off 15, 30, then 60 minutes when none is given. Paused time stops the wall clocks; at `limits.pause_max_min` (360) the session is cancelled, its WIP commit kept, and the group blocks with `pause_timeout`. Three warnings never stop work: a window past `warn.quota_pct` (90), Max drawing overage credits, and a run past `warn.usd_per_run` (5).

---

## 12. Agent roster

| Agent | Weight | Tools | Writes | Pool slot | Purpose |
|---|---|---|---|---|---|
| Builder | Standard | `read`, `search`, `edit`, `write`, `run`, `skill` | Only the group's `files` and test siblings (B1) | Yes | Implements one task per session |
| Reviewer | Heavy | `read`, `search`, `skill`, `diff`, `git_read`, `run` (build, test, lint), `submit_finding` | None; `run` may write and the engine resets it | Yes | Lenses, verified findings, one verdict per task |
| Tester | Light | `read`, `search`, `run` (build and test only) | None; `run` may write and the engine resets it | Yes | Blind pass or fail per acceptance item, with evidence |
| Planner | Heavy | `read`, `search`, `skill`, `lint_backlog` | None; the harness writes files only if lint passes | No | Epic outline, then tasks and items per group |
| Architect | Heavy | `read`, `search`, `skill`, `web` (opt-in, GET) | None | No | Options plus a recommendation |
| Researcher | Standard | Same as architect | None | No | Findings with `file:line` |
| Scout | Light | `read`, `search` (paths only) | None | No | Runs on a local model when its chain allows |

No agent has git write tools, forge credentials or a shell. Only the engine runs worktree add and remove, the lease, `commands.setup`, git commit, push and branch, Check, the group PR, landing and release. No repo-defined agents exist in beta 6.

---

## 13. Deployment view

### 13.1 Platforms

| Platform | Binary | Sandbox | Notes |
|---|---|---|---|
| macOS | `komodo` | seatbelt | APFS clone for dependency copies; no process or memory cap |
| Linux | `komodo` | bwrap | cgroups caps when delegated; `[PENDING G-08]` private loopback per session; `[PENDING G-09]` reflink behaviour per filesystem |
| WSL2 | `komodo` (Linux side) | bwrap | Preferred on Windows; WSL and Windows each probe their own side |
| Windows native | `komodo.exe` | Docker | Full official image per detected language, else `sandbox.image`; `[PENDING G-10]` symlink and junction handling; `[PENDING G-11]` bind-mount copy time |

### 13.2 Topology

```mermaid
flowchart TB
  subgraph Machine["One machine (shared across repos)"]
    BIN["~/.komodo/bin/komodo and versions/<v>/"]
    CFG["~/.komodo/config.json"]
    STATE["~/.komodo/state: pool, link slots, queue, quota, leases"]
    WT["~/.komodo/wt/<repo-id>/<group>/ worktrees"]
    LANG["~/.komodo/cache/lang/<repo-id>/"]
  end
  subgraph Repo["Each repository"]
    AG["AGENTS.md block, CLAUDE.md, .claude/settings.json"]
    RS["<git-common-dir>/komodo: journals, sessions, ledger, cache"]
  end
  subgraph Sessions["Per session"]
    SB["Sandboxed run commands"]
    MCP["Private MCP tool server"]
    TMP["Session HOME and TMPDIR"]
  end
  BIN --> RS
  STATE --> WT
  WT --> SB
  MCP --> SB
```

One binary, standard library only, cross-built for windows, linux and darwin at every gate. Install puts it at `~/.komodo/versions/<v>/komodo` and copies it to `~/.komodo/bin/komodo`. Multi-repo on one binary: each call resolves its repo from cwd or `-C`; the binary, config, Max quota and session pool are shared across repos.

### 13.3 Install, update and health

- **Bootstrap:** `curl -fsSL <repo>/install.sh | sh` on macOS, Linux and WSL; `irm <repo>/install.ps1 | iex` on Windows. The scripts download, verify SHA-256 and run `komodo install --machine-only`.
- **Install is a manifest diff.** It computes the desired manifest, diffs it against `install.json`, applies only the differences, then runs `doctor install`. Rerunning makes 0 changes. Hand-edited owned files survive and are reported; `--force` overwrites. Eight steps: binary; PATH; provider probe (under 2 s, local checks only); `AGENTS.md` block; `CLAUDE.md` import; `.claude/settings.json` (two entries); manifest; optional OS schedule. Never created: a repo config folder, git hooks, `.codex/`, `~/.claude`, `~/.codex`, or any `.claude/` skill, rule or guard hook.
- **Self-update:** only `status`, the engine at run start and `fresh --check` fetch the 1 KB `latest.json`, at most hourly with a 1 s timeout. Any verb that sees a newer `latest` spawns one detached updater, which downloads to `versions/<v>/`, verifies SHA-256 and ed25519, and atomically replaces `bin/komodo`. Live runs keep their own version because the engine starts every child from `versions/<its v>/`. `fresh --restart-runs` checkpoints each run within 2 minutes and resumes it on the new binary at the cost of at most one repeated model turn. Background fresh never downgrades. Off switch: `fresh.auto: false` or `CI=true`. A repo pinned above the binary refuses every verb except `help`, `version`, `fresh`, `install` and `doctor`.
- **Health:** `komodo doctor` checks the install, the sandbox, PATH, the provider catalog with each window's source, endpoint context against the floor, missing toolchain tools, hook drift, long-path settings on Windows, the Docker daemon, and the backlog grammar. `doctor --sandbox <cmd>` runs a command under the policy.

---

## 14. Security view

### 14.1 Threats and controls

| Threat | Stopped by |
|---|---|
| Agent escapes its worktree | Guard row G2 (containment, `..`, symlinks); the sandbox |
| Agent leaks or reads secrets | G4 (secret files and patterns); sandbox environment allowlist and home-folder read block; Check's secret scan; HTTP and ledger redaction |
| Agent pushes, merges or force-pushes | No git write tools; R1 (no class runs git); orchestrator rules O1 to O3; only Ship writes git |
| Tampered or stale binary | SHA-256 and ed25519 verification on every fresh; repo pin |
| Runaway cost or loops | Session supervisor, error recovery bounds, budgets |
| Poisoned cache | Cache outside worktrees, verified on read; Komodo-owned language caches per repo |
| Malicious external MCP | Opt-in allowlist per agent, off by default, no credentials |
| Stale engine lands over newer work | Fence enforced by git compare-and-swap; action idempotency keys |
| Prompt steering through a skill or repo text | Steering-phrase scan at the gate and in `doctor`; no rule text in prompts; the `text` policy row |
| Tampered config or binary at run time | Config hash guard; signatures; G3 protects both files |

### 14.2 Guard summary

Rules are data in `embed/policy.json` with scopes, read by the guard, the gate and the LLD roster tables. Every tool call is checked in-process before it runs; a refusal names the rule and the allowed alternative; the third refusal under one rule ends the session. `needs_scope` is the only refusal a person or the orchestrator can turn into an allow.

| Family | Rows | Binds to |
|---|---|---|
| G1 to G6 | Unlisted tool; worktree escape; protected paths; secret reads and secret-shaped writes; attribution trailers; the refusal limit | All agents (write rows bind to writers only) |
| R1 | Only declared command classes, as `{class, args[]}`, no shell | Any agent with `run` |
| B1 | Writes only to the group's `files` and test siblings; otherwise stop with `needs_scope` | Builder |
| O1 to O10 | Critical-ref writes, force pushes, hand merges, leased worktrees, stash, `gh pr`, faked terminals, human-only verbs, `--no-verify` | The orchestrator, as text in `AGENTS.md`; enforced on the harness side by verb refusals and the terminal check |

### 14.3 Sandbox summary

| OS | Sandbox | Shape |
|---|---|---|
| macOS | `sandbox-exec` (seatbelt) | Reads outside home allowed; inside home only the worktree, repo git dir, Komodo caches and toolchains; writes only to worktree, session temp and language caches; network denied except loopback |
| Linux, WSL | `bwrap` | `/` read-only; home an empty tmpfs with the same folders bound back; `--unshare-net`; `--die-with-parent`; refuses with the fix when user namespaces are blocked |
| Windows native | Docker (when WSL is absent) | `--network none`; only the worktree, session temp and language-cache volumes writable; with neither WSL nor Docker every work verb refuses |

The sandbox is `required` on every OS. Every `run` gets a 10-minute timeout, a 4 KB reply cap, 512 processes and 8 GB (cgroups or Docker; macOS has no cap and `doctor` says so), and a whole-tree kill. Intake runs `commands.setup` with network on; every `run` keeps it off. Loopback is always allowed.

### 14.4 Release integrity

Releases are signed with ed25519 by a key at `~/.komodo/release.key` (mode 0600) on the owner's machine, with one backup in a password manager. The current and next public keys are embedded in the binary; the `next` key is set at the first release so a leak is survivable by rotation. The bootstrap scripts verify SHA-256 over HTTPS; every later `fresh` verifies the ed25519 signature. Enterprise signing (KMS or keyless, rollback refusal) is a later minor, G-21.

---

## 15. Quality assurance view

### 15.1 Four gates, three of them free

| Gate | Evidence | Tokens |
|---|---|---|
| Lint and coverage | Check; `check.patch_coverage` (80) on changed lines, blocking for Go, warning for other languages | 0 |
| Success | `done_when` rerun per task | 0 |
| Regression | Baseline diff at the base commit plus the impact set | 0 |
| Behaviour | The tester's blind pass over EARS acceptance items | Model |

### 15.2 Check (ground truth)

| Check | Rule |
|---|---|
| Baseline | Intake records the `test` class result at the base commit, cached by tree hash. A test that passed at base and fails at head is a regression and blocks; one failing at both is pre-existing and warns. |
| Impact set | Go repos run the tests of every package that imports a changed package; other languages run the full suite. The full suite always runs at the epic gate. |
| Flake rule | Any failing test is rerun twice alone; two more failures confirm it; a split reports `flaky` without blocking and is counted per repo. |
| Scope | Every changed file is in the group's `files` or is a test sibling. |
| Secrets | The diff is scanned for secret patterns. |
| Cache | Results are keyed by tree hash plus command; an unchanged tree skips the rerun. |

### 15.3 Review and the tester

- **Lenses are rule catalogs applied by scope.** Ten lenses; a lens runs only when a changed file matches its `applies`. Thorough mode runs every applicable lens in parallel plus a cold pass; economy runs one merged lens. `blocker` and `major` block; `minor` is reported and counted.
- **Standards compile to rules.** Each `- **Never` and `- **Always` line in a standards skill becomes a rule ID the reviewer cites.
- **Findings carry evidence.** A finding blocks only if Go finds its quoted line at `file:line` in the head or the diff. Failing findings are dropped and logged.
- **One verdict per task.** The reviewer returns `met`, `not_met` or `brief_incomplete` for every task ID exactly once; the tester returns `confirmed` or `not_confirmed` per task and pass or fail per item.
- **The tester is blind by construction.** Its sandbox profile cannot see the builder's worktree; its brief holds only engine facts from the backlog at the base commit.
- **Precision ledger.** Per rule and model: submitted, verified, fixed, disputed. `doctor` lists rules under 50% verified; a rule under 30% for 60 days is a plan change. `[PENDING G-16]` the catalogs' content, authored in phase 7.
- **Re-review** covers only the delta; cached findings cost 0 tokens. The tester reruns every item.

### 15.4 Gate and self-checks (Komodo's own repo)

`komodo gate` runs build, tests and cross-builds; import direction and strict decode of every agent file and job template; the guard table and prefix-hash pin; event catalog completeness; docs drift; the component map; the "no silent gaps" rule; every generated artifact against its source; and timing targets. It must finish under 3 minutes locally; `--fast` skips cross-builds and timing. `[PENDING G-19]` the 10,000-event replay under 1 s and the 50 ms short-verb budget are targets, measured in phase 6.

---

## 16. Operations view

### 16.1 Scheduling

There is no daemon. `komodo jobs tick` is idempotent and is one engine step: it enqueues due jobs, fires due pause timers and drives the pool once. `install --schedule` writes a launchd, cron or Task Scheduler entry every 5 minutes. A job template names an agent subset, brief slots and an interval, never limits. Overlap is prevented by the job's lease. No jobs ship by default; one example template exists.

### 16.2 Error recovery and the loss bound

Every failure has a detector, an action, a bound and a final state; nothing loops without a counter. The startup sweep kills orphans by pid file, frees expired leases, re-enqueues queue items with no lease and emits `session.end crash` for dead sessions. A crash is contained to its group. `resume` tries the provider's resume handle first; if that fails, a fresh session gets the brief, the WIP diff and the last summary, and the ledger logs `resume_lost`. Blocked never auto-retries; a group blocked 3 times for one reason is flagged `needs redesign`.

### 16.3 Observability

`status [--watch]` shows the pool, stages, budgets, blocks and the group graph. `status --wait` blocks until the next wake event and is the orchestrator's background loop. `status --explain` prints the last 10 events with reasons. `status --metrics` is the scorecard. Every session's model, measured context and cost class are in the ledger and in `status`.

### 16.4 Runbook summary

| Situation | Operator action |
|---|---|
| A group is Blocked | Read the reason in `status`; fix the cause; `komodo resume <run>` |
| A builder asks for scope | `komodo scope approve|deny <group> --paths --reason` (the orchestrator usually answers) |
| A provider limit | Nothing; the group pauses and resumes itself; `resume` retries sooner |
| A run must stop | `komodo stop <run>`; later `resume` |
| A run must be dropped | `komodo abandon <run> --reason` |
| The config file was hand-edited | `komodo config --accept` |
| The binary is stale or pinned | `komodo fresh`; `fresh --restart-runs` to move live runs |
| After merging the epic PR | `komodo sync` |

---

## 17. Interfaces

### 17.1 Command surface

| Tier | Who calls it | Verbs |
|---|---|---|
| Shared | Human and orchestrator | `help`, `version`, `doctor`, `sync`, `add backlog`, `add changelog`, `plan`, `run`, `status`, `skill`, `scope`, `resume`, `stop`, `abandon`, `jobs list|show|tick`, `cache stats`, `gate` (except `--install*`), `config` reads |
| Terminal-only | Human; refused without a controlling terminal | `install`, `uninstall`, `fresh`, `config` writes, `doctor --fix`, `jobs run|pause|resume`, `cache clear`, `gate --install`, `gate --install-hooks`, `release` |
| Internal | The engine | `internal tools`, `internal fresh-bg` |

Every verb: five processing steps (parse, validate, resolve, dispatch, render); a verb that fails before dispatch has made 0 writes; every writing verb is idempotent and takes `--dry-run`; every verb accepts `--input <file|->` as one JSON object with explicit flags winning.

### 17.2 Envelope and exit codes

`{v: 1, ok, verdict, report, data, error{code, reason, fix}}`. `report` is human text already formatted to `output.profile`, so the orchestrator relays it verbatim. Exit codes: 0 ok, 1 refused, 2 usage, 3 blocked, 4 internal. `--json` is on automatically without a terminal.

### 17.3 The orchestrator contract

Install writes a Komodo block into `AGENTS.md` that tells any host session: run `komodo help --json` first; your verbs are the shared set; `install`, `fresh`, config writes, `gate --install` and `release` belong to the person; run `status --wait` in the background; relay `report` verbatim. The rules O1 to O7, O9 and O10 and the output style follow in the same block. A Claude `SessionStart` hook runs `komodo status` so a new session sees state on line one.

---

## 18. Architecture decisions

### 18.1 Decisions the owner made (2026-10-09)

| ID | Decision | Enterprise target |
|---|---|---|
| G-01 | Signing key: local file plus one backup copy; `next` key set at the first release | G-21: KMS or keyless signing; rollback refusal; trigger: the first machine the owner does not own runs `fresh` |
| G-05 | `providers.json` is the only model registry; a 404 fails over through the chain | G-22: a central model gateway with `retired_on` dates and 30-day warnings; trigger: 3 or more machines, or 2 people sharing one cap |
| G-15 | The orchestrator answers `scope approve` after two engine denials: protected paths and paths another live group owns | G-23: path ownership rows in `policy.json`, a named human for other teams' paths, a widening digest; trigger: the first repo with 2 teams or customer data |

### 18.2 Decisions recorded with their rejected alternative

| Decision | Rejected alternative | Reason recorded |
|---|---|---|
| Every loop has a counter | Temporal's default unlimited retry with 2× backoff | The prototype's retries were effectively unbounded |
| `claude -p` with pinned flags | `--bare` mode | Bare mode reads only an API key and never the subscription login |
| Fence enforced by git compare-and-swap | A reread before each push | A reread leaves a window between the read and the write |
| Measured context per session | Typed context per model | A larger typed window than the real one lets a server cut the prompt silently |
| Shared Rust target dir per group | Copying `target/` to the tester worktree | Fingerprints are keyed by package path, so a copy rebuilds anyway `[PENDING G-12]` |
| Language caches owned by Komodo | The user's `~/.npm`, `~/.cargo/registry`, Go and pip caches | Agent code could plant files the user's own builds run |
| Session caps through cgroups or Docker only | The per-user process limit | It counts every process the user runs and made `fork` fail at 512 |
| Machine-wide session pool | Per-repo lanes | Quota and link limits are machine-wide; lanes could not see each other |
| Repair folded into Build | A separate Repair stage | A return resumes the task's own session; a stage added nothing |
| No rule text in worker prompts | Rules in `core.md` and `agent.json` `rules` | The harness enforces rules where it acts; prompt rules are advisory |

### 18.3 Defaults chosen by the design pass, awaiting the owner's confirmation

| Default | Where it applies |
|---|---|
| `fable` opt-in only | Agent model override |
| Haiku 5.5 as the light weight | Model catalog |
| `check.patch_coverage` 80, blocking for Go only | Check |
| Group wall hard limit 720 minutes | Configuration |
| Install flags trimmed to 8 | Installer |
| B2 moved from a guard row to lint | Guardrails, backlog lint |
| 10 lenses | Review |
| EARS acceptance items | Backlog grammar |
| The two-pass planner | Planning |
| `--permission-prompts none` in the launch line | Provider adapters |
| The 4 binary-lifecycle Vet lines (hot swap, check points, restart window, prerelease flag) | Binary lifecycle |
| The shared Rust target directory per group | Sandbox |

---

## 19. Quality requirements

### 19.1 Quality scenarios

| Attribute | Scenario | Measure |
|---|---|---|
| Performance | A person runs `help`, `version`, a config read or `skill` | Under 50 ms, no network, no repo scan beyond marker stats `[PENDING G-19]` |
| Performance | Install reruns the provider probe | Five checks in parallel under 2 s; a no-diff rerun under 1 s |
| Performance | `status` renders a repo with 1,000 ledger rows or 50 runs | Under 100 ms; under 10 ms |
| Performance | The engine replays a 10,000-event journal | Under 1 s; resume bounded by 50 events from the snapshot `[PENDING G-19]` |
| Performance | `komodo gate` runs locally | Under 3 minutes |
| Durability | The engine is killed with `kill -9` mid-run and resumed | 0 repeated paid sessions; at most one model turn and no committed edit lost |
| Durability | Two engines race to land one group | Exactly 1 landing; the loser ends in `lease.lost` |
| Safety | A `run` writes outside the worktree, reads `~/.ssh`, or connects to a public address on any OS | Refused by the sandbox |
| Safety | `ANTHROPIC_API_KEY` is set beside a Max login | The session runs on `apiKeySource: none`; 0 paid calls |
| Cost | 20 API sessions of one agent | Cache hit rate at or above 50%, else a fixture fails `[PENDING G-17]` |
| Capacity | Default limits | 6 sessions machine-wide, 3 per link, 1 per loopback endpoint; hard limits 16 and 8 `[PENDING G-04]`, `[PENDING G-07]` |
| Portability | Every gate run | Cross-builds for windows, linux and darwin succeed; the fixture group ships on macOS and Windows |

### 19.2 Quality tree

| Quality goal | Attributes it owns |
|---|---|
| Safety | Containment, secret handling, git write control, billing control, prompt steering resistance |
| Durability | Journal integrity, replay, fencing, orphan control, lease expiry |
| Determinism | Schema enforcement, template rendering, event catalog completeness, generated-artifact gates |
| Cost control | Chain order, cache prefix, budgets, pool adaptation, warnings |
| Portability | Sandbox per OS, copy mechanics per filesystem, path and line-ending handling, cross-builds |

---

## 20. Risks and technical debt

### 20.1 Risks

| Risk | Impact if it materialises | Mitigation in the design | Closes by |
|---|---|---|---|
| The probe cannot capture the `system/init` line without a model turn | Billing check 3 costs one turn per probe | The probe records one line per CLI version; a version without the field fails the link at probe time | `[PENDING G-02]` spike, phase 4 |
| A 24 GB machine cannot run Qwen3.6-27B at 262,144 context | The default backstop is unusable on small machines | The floor is a config key; `doctor` names the endpoint that fails it | `[PENDING G-03]` spike, phase 4 |
| Ollama serves 1 request per model | `link_sessions.local` stays 1 and the backstop serialises | `doctor` reads `OLLAMA_NUM_PARALLEL` and names the fix | `[PENDING G-04]` spike, phase 4 |
| The Max 5-hour window drains faster than 3 sessions per link assumes | More pauses than planned | The pool halves on a rate-limit error; the ledger measures the real window | `[PENDING G-07]` measurement, phase 7 |
| bwrap does not give a private loopback per session | Two Verify test suites collide on a fixed port on Linux | macOS's per-repo test lock already exists as the fallback | `[PENDING G-08]` spike, phase 2 |
| Copy mechanics differ per filesystem | Slow or broken tester worktrees on ext4, NTFS or Docker | A copy over 60 s warns; `doctor` reports the mechanism | `[PENDING G-09]`, `[PENDING G-10]`, `[PENDING G-11]`, `[PENDING G-12]` spikes, phase 2 |
| The steering-phrase list is incomplete | A skill or addendum steers a worker | The list is data; `doctor` warnings tune it | `[PENDING G-13]`, phase 3 |
| A nudge cannot reach a turn stuck in a tool call | An idle session runs to its wall clock | The 5-minute grace and the wall clock bound it | `[PENDING G-14]`, phase 5 |
| Lens catalogs are not yet authored | Review quality depends on content that does not exist | The precision ledger flags rules under 50% verified | `[PENDING G-16]`, phase 7 |
| Prompt cache hit rate below 50% on the API link | Cost per group rises | Prefix order and four breakpoints are designed for it; a fixture measures it | `[PENDING G-17]`, phase 7 |
| Repo map patterns are unproven for TypeScript, Python and Rust | Planner briefs for those languages are weaker | Go via `go/ast` is exact; other languages are pattern files that can be tuned | `[PENDING G-18]`, phase 7 |
| Timing targets are unmeasured | Short verbs or replay slower than designed | The gate times them from phase 6 | `[PENDING G-19]` |
| A single owner holds the signing key | Loss of the key or the machine blocks releases | One backup in a password manager; the `next` key allows rotation | G-21 at the enterprise trigger |

### 20.2 Technical debt

| Debt | Statement | Retirement |
|---|---|---|
| Beta 5 artifacts on the owner's machine | Global hooks, 36 skill overrides and 5 skills call beta 5 verbs; beta 6 would answer them with exit 2 | Hand edits before phase 2; `doctor install` confirms 0 Komodo hooks at phase 9 |
| The OpenAI path is scoped but not implemented | `codex-cli` and `openai-api` refuse with `not yet` | Later minor `[PENDING G-20]` |
| Enterprise custody, registry and scope ownership | Single-machine designs for signing, model IDs and scope approval | G-21, G-22, G-23 at their triggers |
| `PLAN.md` as the interim source of truth | The plan is removed once phase 1 lands; its gap register must survive in this document set | The gate's "no silent gaps" rule from phase 1 |

---

## 21. Delivery roadmap

| Phase | Wave | Scope | Exit proof |
|---|---|---|---|
| 1 | Design docs | `prd`, `hld`, `lld`, ADR 0001, epic-1 | Owner approves the docs; epic-1 lints in phase 2 |
| 2 | Foundations | CLI skeleton, configuration, HTTP stack, sandbox, cache, version, backlog lint, gate core; a `claude -p` plus MCP spike | Core tests pass on 3 OSes; `doctor backlog` passes on epic-1; the spike makes one MCP tool call |
| 3 | Model surface | Agent registry, skills, rules and output, guardrails | Guard table and strict decode pass |
| 4 | Model I/O | Provider adapters (`claude -p`, local, Anthropic API), tool runtime, brief builder | A fixture task finishes through `claude -p` and a local model |
| 5 | Runtime | Workspace, supervisor, observability, event bus | Timeout, idle and kill proofs pass with 0 orphans |
| 6 | Engine | Orchestration engine, error recovery, Check, budgets, ship | The fixture group lands on its epic branch |
| 7 | Quality and scale | Review and tester, scheduler, planner | Review catches the seeded bug; a job ticks once per window |
| 8 | Edges | Installer, fresh, release, docs drift | Fresh-repo smoke test on macOS and Windows |
| 9 | User docs and cutover | README, CONTRIBUTING, AGENTS/CLAUDE, CHANGELOG, templates, cutover | Docs-drift gate passes; `doctor install` finds 0 stale references |

Phases 2 to 6 are built by hand in plain Claude Code sessions. From phase 7, `komodo gate --install` places a dev build and beta 6 runs its own groups. Beta 5 retires before phase 2 by removing its hooks, skill overrides and skills from `~/.claude`.

---

## Appendix A. Traceability

| Component | Plan ID | Package | Phase |
|---|---|---|---|
| CLI processor | S01 | `cli` | 2 |
| Installer | S02 | `host` | 8 |
| Binary lifecycle | S03 | `host`, `core` | 2, 8 |
| Configuration | S04 | `core` | 2 |
| Provider adapters | S05 | `provider` | 4 |
| HTTP stack | S06 | `core` | 2 |
| Agent registry | S07 | `agent` | 3 |
| Tool runtime | S08 | `tools`, `provider` | 4 |
| Skill loader | S09 | `agent` | 3 |
| Rules and output contract | S10 | `agent` | 3 |
| Brief builder | S11 | `agent` | 4 |
| Guardrails | S12 | `safety` | 3 |
| Sandbox and process control | S13 | `safety` | 2 |
| Orchestration engine | S14 | `engine` | 6 |
| Workspace and leases | S15 | `engine` | 5 |
| Session supervisor | S16 | `engine` | 5 |
| Scheduler and job templates | S17 | `engine` | 7 |
| Error recovery | S18 | `engine` | 6 |
| Check | S19 | `quality` | 6 |
| Review and evidence | S20 | `quality` | 7 |
| Token and resource management | S21 | `ops`, `agent` | 6 |
| Cache store | S22 | `core` | 2 |
| Runtime observability | S23 | `ops`, `cli` | 5 |
| Event bus | S24 | `ops` | 5 |
| Backlog and planning | S25 | `repo` | 2, 7 |
| Ship | S26 | `repo` | 6, 8 |
| Gate and self-checks | S27 | `repo`, `safety` | 2, 8 |

## Appendix B. References

| Reference | Use in this document |
|---|---|
| arc42 template, https://arc42.org/overview | Section structure: introduction and goals, constraints, context, solution strategy, building blocks, runtime, deployment, cross-cutting views, decisions, quality requirements, risks |
| ISO/IEC/IEEE 42010:2022 | Stakeholders, concerns and views framing (§4.7 and the view sections) |
| Design Docs at Google, https://www.industrialempathy.com/posts/design-docs-at-google/ | Goals and non-goals, decisions with rejected alternatives |
| Temporal workflows, https://docs.temporal.io/workflows | Pure decider and journaled activities |
| Distributed leases with fencing tokens, https://hackernoon.com/building-distributed-leases-with-consensus-heartbeats-and-fencing-tokens | Fence enforced by the resource |
| OpenHands, https://arxiv.org/pdf/2407.16741 | Event stream as the agent's spine |
| Codex CLI security, https://developers.openai.com/codex/security | Sandbox shape per OS, network off by default |
| AWS exponential backoff and jitter, https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter | Full-jitter retries |
| Aider repo map, https://github.com/pdavis68/RepoMapper | Ranked repo map |
| Kiro spec-driven development, https://verdent.ai/guides/agents/kiro-spec-driven-development | EARS acceptance items |
| Google Testing Blog on coverage, https://testing.googleblog.com/2020/08/code-coverage-best-practices.html | Coverage bands |
| Claude Code headless docs, https://code.claude.com/docs/en/headless | Session launch, resume, structured output, signals |
| Anthropic prompt caching, https://platform.claude.com/docs/en/build-with-claude/prompt-caching | Cache breakpoints and pricing |
| Anthropic rate limits, https://platform.claude.com/docs/en/api/rate-limits | Pool and link defaults, limit classes |
| Ollama FAQ, https://docs.ollama.com/faq | Context length and parallelism defaults |
