# Komodo 1.0.0-beta.6 — Low-Level Design

## 1. Table of contents

- [2. Glossary](#2-glossary)
- [3. Document metadata](#3-document-metadata)
  - [3.1 Owners](#31-owners)
  - [3.2 Current controls](#32-current-controls)
  - [3.3 Revision history](#33-revision-history)
  - [3.4 Related documents](#34-related-documents)
- [4. Introduction](#4-introduction)
  - [4.1 Purpose](#41-purpose)
  - [4.2 Scope](#42-scope)
  - [4.3 How to read this document](#43-how-to-read-this-document)
  - [4.4 Relationship to the HLD](#44-relationship-to-the-hld)
- [5. Design constraints and conventions](#5-design-constraints-and-conventions)
  - [5.1 Constraints](#51-constraints)
  - [5.2 Naming and identifiers](#52-naming-and-identifiers)
  - [5.3 Document conventions](#53-document-conventions)
- [6. Code structure](#6-code-structure)
  - [6.1 Packages](#61-packages)
  - [6.2 Import order](#62-import-order)
  - [6.3 Embedded assets (`embed/`, compiled into the binary)](#63-embedded-assets-embed-compiled-into-the-binary)
  - [6.4 Runtime state](#64-runtime-state)
- [7. Command-line interface](#7-command-line-interface)
  - [7.1 Processing pipeline](#71-processing-pipeline)
  - [7.2 Envelope and exit codes](#72-envelope-and-exit-codes)
  - [7.3 Global flags](#73-global-flags)
  - [7.4 Verbs](#74-verbs)
  - [7.5 Tier enforcement](#75-tier-enforcement)
  - [7.6 Performance](#76-performance)
- [8. Configuration](#8-configuration)
  - [8.1 Layers and write path](#81-layers-and-write-path)
  - [8.2 Flat keys (33)](#82-flat-keys-33)
  - [8.3 Agent group (`komodo config agent <name> …`)](#83-agent-group-komodo-config-agent-name-)
  - [8.4 Endpoint group (`komodo config endpoint <name> …`)](#84-endpoint-group-komodo-config-endpoint-name-)
  - [8.5 Environment variables](#85-environment-variables)
  - [8.6 Command detection](#86-command-detection)
  - [8.7 Capacity profile per link](#87-capacity-profile-per-link)
- [9. Event model](#9-event-model)
  - [9.1 Envelope](#91-envelope)
  - [9.2 Catalog (32 types)](#92-catalog-32-types)
  - [9.3 Journals and snapshots](#93-journals-and-snapshots)
  - [9.4 The outbox](#94-the-outbox)
  - [9.5 Epochs](#95-epochs)
- [10. Group state machine](#10-group-state-machine)
  - [10.1 Transitions](#101-transitions)
  - [10.2 Paused versus Blocked](#102-paused-versus-blocked)
  - [10.3 Block reasons](#103-block-reasons)
- [11. Orchestration engine internals](#11-orchestration-engine-internals)
  - [11.1 Concurrency model](#111-concurrency-model)
  - [11.2 Work queue](#112-work-queue)
  - [11.3 Session pool](#113-session-pool)
  - [11.4 Run order and convergence](#114-run-order-and-convergence)
  - [11.5 Run flags](#115-run-flags)
- [12. Workspace and leases](#12-workspace-and-leases)
  - [12.1 Worktrees](#121-worktrees)
  - [12.2 Lease](#122-lease)
  - [12.3 Fence](#123-fence)
  - [12.4 Saved work](#124-saved-work)
  - [12.5 Shutdown and the parent guard](#125-shutdown-and-the-parent-guard)
  - [12.6 Startup sweep](#126-startup-sweep)
- [13. Session supervisor](#13-session-supervisor)
  - [13.1 Lifecycle](#131-lifecycle)
  - [13.2 Controls](#132-controls)
  - [13.3 Exit reasons](#133-exit-reasons)
  - [13.4 Resume](#134-resume)
- [14. Agent registry](#14-agent-registry)
  - [14.1 `agent.json` (what Go reads)](#141-agentjson-what-go-reads)
  - [14.2 `prompt.md` (what the model reads)](#142-promptmd-what-the-model-reads)
  - [14.3 Roster and limits](#143-roster-and-limits)
- [15. Tool runtime](#15-tool-runtime)
  - [15.1 Registry and transports](#151-registry-and-transports)
  - [15.2 Harness tool loop (API links)](#152-harness-tool-loop-api-links)
  - [15.3 Tools and caps](#153-tools-and-caps)
  - [15.4 Per-agent tool matrix](#154-per-agent-tool-matrix)
  - [15.5 Host facts the server is built against (Claude Code 2.1.295)](#155-host-facts-the-server-is-built-against-claude-code-21295)
- [16. Provider adapters](#16-provider-adapters)
  - [16.1 Interface](#161-interface)
  - [16.2 Links](#162-links)
  - [16.3 Schema enforcement per link](#163-schema-enforcement-per-link)
  - [16.4 Measured context](#164-measured-context)
  - [16.5 Model catalog (`embed/models.json`, about 10 entries)](#165-model-catalog-embedmodelsjson-about-10-entries)
  - [16.6 Failover chain](#166-failover-chain)
  - [16.7 Billing check (fails closed)](#167-billing-check-fails-closed)
  - [16.8 Limits, pauses and warnings](#168-limits-pauses-and-warnings)
  - [16.9 `claude-cli` launch line](#169-claude-cli-launch-line)
- [17. HTTP stack](#17-http-stack)
- [18. Skill loader](#18-skill-loader)
- [19. Rules and output contract](#19-rules-and-output-contract)
  - [19.1 Where rules are enforced](#191-where-rules-are-enforced)
  - [19.2 Output mapping (`embed/output/`)](#192-output-mapping-embedoutput)
  - [19.3 Template pipeline](#193-template-pipeline)
  - [19.4 Code list](#194-code-list)
  - [19.5 No leaks](#195-no-leaks)
- [20. Brief builder](#20-brief-builder)
  - [20.1 Assembly order (most stable first)](#201-assembly-order-most-stable-first)
  - [20.2 Slots (20)](#202-slots-20)
  - [20.3 Ceiling per link](#203-ceiling-per-link)
  - [20.4 Context rules](#204-context-rules)
- [21. Guardrails](#21-guardrails)
  - [21.1 Policy row schema (`embed/policy.json`)](#211-policy-row-schema-embedpolicyjson)
  - [21.2 Rows every agent inherits](#212-rows-every-agent-inherits)
  - [21.3 Run and builder rows](#213-run-and-builder-rows)
  - [21.4 Orchestrator rows (text in `AGENTS.md`, any host)](#214-orchestrator-rows-text-in-agentsmd-any-host)
  - [21.5 Mechanics](#215-mechanics)
- [22. Sandbox and process control](#22-sandbox-and-process-control)
  - [22.1 Three layers](#221-three-layers)
  - [22.2 Per OS](#222-per-os)
  - [22.3 Caches and toolchains](#223-caches-and-toolchains)
  - [22.4 Read-only agents' `run`](#224-read-only-agents-run)
  - [22.5 Test sandboxes and copies](#225-test-sandboxes-and-copies)
  - [22.6 Docker backend (native Windows)](#226-docker-backend-native-windows)
- [23. Scheduler and job templates](#23-scheduler-and-job-templates)
- [24. Check](#24-check)
  - [24.1 Scope requests](#241-scope-requests)
- [25. Review and the tester](#25-review-and-the-tester)
  - [25.1 Lenses](#251-lenses)
  - [25.2 Mechanics](#252-mechanics)
  - [25.3 The tester](#253-the-tester)
- [26. Error recovery](#26-error-recovery)
  - [26.1 Failure table](#261-failure-table)
  - [26.2 Rules](#262-rules)
- [27. Token and resource management](#27-token-and-resource-management)
- [28. Cache store](#28-cache-store)
- [29. Runtime observability](#29-runtime-observability)
  - [29.1 Ledger views](#291-ledger-views)
  - [29.2 Scorecard (`status --metrics`)](#292-scorecard-status---metrics)
  - [29.3 Live views](#293-live-views)
- [30. Event bus](#30-event-bus)
- [31. Backlog and planning](#31-backlog-and-planning)
  - [31.1 Grammar](#311-grammar)
  - [31.2 Lint (`komodo doctor backlog`)](#312-lint-komodo-doctor-backlog)
  - [31.3 Planner](#313-planner)
- [32. Ship and release](#32-ship-and-release)
- [33. Installer](#33-installer)
  - [33.1 Steps](#331-steps)
  - [33.2 Provider probe](#332-provider-probe)
  - [33.3 The Komodo block in `AGENTS.md`](#333-the-komodo-block-in-agentsmd)
  - [33.4 Bootstrap](#334-bootstrap)
- [34. Binary lifecycle](#34-binary-lifecycle)
- [35. Gate and self-checks (`komodo gate`, Komodo repo only)](#35-gate-and-self-checks-komodo-gate-komodo-repo-only)
- [36. Edge cases](#36-edge-cases)
- [37. Verification plan](#37-verification-plan)
- [38. Cutover from beta 5](#38-cutover-from-beta-5)
- [39. Gap register](#39-gap-register)
- [Appendix A. Traceability to the plan](#appendix-a-traceability-to-the-plan)
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
| Document | Low-Level Design (LLD) |
| System | Komodo 1.0.0-beta.6, standalone model-agnostic Go harness |
| Version | 0.1, draft |
| Status | Awaiting architecture sign-off by the document owner |
| Date | 2026-10-09 |
| Source of truth | `PLAN.md` at commit `ba4ff823` on `feat/1.0.0-beta.6`; `drafts/README.md` for brief slots |
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
| `docs/hld.md` | The High-Level Design this document details |
| `PLAN.md` | Interim source of truth; removed once phase 1 lands |
| `docs/prd.md` | Product requirements, phase 1 deliverable |
| `docs/decisions/0001-standalone-harness.md` | First architecture decision record, phase 1 deliverable |
| Appendix B | External references |

**Placeholder convention.** Text in the form `[PENDING G-NN]` marks a fact the plan has not yet verified or a decision the owner has not yet confirmed. No placeholder hides a proposal; each stands in for content that is decided but not yet proven.


---

## 4. Introduction

### 4.1 Purpose

This document specifies how each component of Komodo beta 6 is built: its files, schemas, algorithms, limits, failure handling and edge cases. It is the implementation contract for phases 2 to 9 and the source the reviewer's `spec` lens checks a diff against.

### 4.2 Scope

Every component in the HLD's building block view, at the level of file paths, field tables, state transitions, command lines and numeric limits. Items deferred to a later minor version appear only where a beta 6 interface must already account for them (the OpenAI links, enterprise custody targets).

### 4.3 How to read this document

| Reader | Start at |
|---|---|
| Owner reviewing a component | The component's section (Appendix A maps plan IDs to sections), then §36 edge cases |
| Builder of a task group | The component's section, §9 code structure, §35 gate checks |
| Reviewer applying the `spec` lens | The section a task cites, plus §26 error recovery for the row it touches |
| Operator diagnosing a run | §10 state machine, §26 error recovery, §29 observability, §36 edge cases |

### 4.4 Relationship to the HLD

The HLD states what each component is for and how the components cooperate. This document states how each one works inside. Where both describe the same mechanism, this document carries the numbers and the HLD carries the rationale; a conflict between them is a defect to fix in both.

---

## 5. Design constraints and conventions

### 5.1 Constraints

| Constraint | Statement |
|---|---|
| Dependencies | Standard library only; `go.mod` lists no modules |
| Packages | One package per layer, 11 in all; a new package needs a new layer |
| Files | A file splits at about 600 lines; `_test.go` sits beside its source; about 85 files |
| Imports | One downward order, enforced by the gate; higher needs enter as parameters |
| Embedded assets | Everything a model reads compiles into the binary with `//go:embed`; nothing is parsed from disk at run time |
| Strict decoding | Every JSON the binary reads from its own assets uses `DisallowUnknownFields` and a schema version `v` |
| Side effects | Every engine action is journaled and idempotent by a declared key; nothing else changes group state |
| Security rows | Never configuration; the only tunable is the refusal count |

### 5.2 Naming and identifiers

| Item | Convention |
|---|---|
| Config keys | Dotted, lower case: `limits.pool_max`; groups as `agents.<name>` and `endpoints.<name>` |
| Event types | `family.name`: `session.end`, `lease.lost` |
| Result and reason codes | Snake case from one enum: `needs_scope`, `pause_timeout` |
| Guard rows | `G1`…`G6`, `R1`, `B1`, `O1`…`O10` |
| Rule IDs | `<lens>.<n>` for lens rules; `std.<skill>.<n>` for standards rules |
| Branches | `komodo/<epic>` and `komodo/<epic>/<group>` |
| Hidden refs | `refs/komodo/groups/<group>` |
| Runs | A run id from `run`; `job-<name>-<n>` for scheduled jobs |
| Sessions | A UUID chosen by Komodo |
| Gaps | `G-NN`, one row each in the gap register |

### 5.3 Document conventions

- `[PENDING G-NN]` marks a fact not yet verified or a decision not yet confirmed; the gap register (§39) lists every one with its closing phase.
- Defaults are stated once, in the table that owns them; other sections refer to the key.
- "Writers" means any agent with `edit` or `write`; "runners" means any agent with `run`.
- Numbers are the design's targets; a number marked `[PENDING G-19]` is unmeasured.

---

## 6. Code structure

### 6.1 Packages

| Package | Layer | Holds |
|---|---|---|
| `cmd/komodo` | Entry | One file, `main.go`, calling `cli.Run(os.Args)` |
| `internal/cli` | Interface | Verb table, dispatch, envelope, exit codes, pin check, help generation, every verb as a thin call |
| `internal/host` | Interface | Probe, install, Claude and Codex detection, fresh, OS schedule entries, doctor |
| `internal/engine` | Orchestration | `Drive` and `Next()`, stages, intake, build, verify, pool, queue, state, worktree, shutdown, session, schedule, limits, recover |
| `internal/quality` | Quality | Check, review, tester, evidence |
| `internal/repo` | Delivery | Backlog, lint, graph, repo map, ship, git, PR, release, gate |
| `internal/provider` | Model I/O | Provider interface, billing, harness tool loop, Claude CLI, Codex stub, Anthropic API, OpenAI-compatible |
| `internal/tools` | Model I/O | Tool registry, MCP server, file tools, run, git read, submit tools, skill and web |
| `internal/ops` | Economy and ops | Budget, ledger, bus, wake |
| `internal/agent` | Model I/O and Context | Agent and job template structs, skills, rules, render, brief, trim |
| `internal/safety` | Safety | Guard, rows, paths, shell, guard table, process control, sandboxes |
| `internal/core` | Plumbing | Config, detect, HTTP, cache, locks, version, fs, test home |

Rules: a file splits at about 600 lines; a new package needs a new layer; `_test.go` files sit next to their sources. About 85 files in 11 packages.

### 6.2 Import order

`cli` → `engine` → `quality` → `host` → `repo` → `provider` → `tools` → `ops` → `agent` → `safety` → `core`. A package may skip levels. Higher needs come in as parameters. The gate refuses any import that points up.

### 6.3 Embedded assets (`embed/`, compiled into the binary)

| Path | Contents |
|---|---|
| `agents/<name>/{agent.json, prompt.md}` | Seven agents: builder, reviewer, planner, tester, architect, researcher, scout |
| `jobs/` | Job templates (none shipped by default) |
| `rules/orchestrator.md`, `rules/backlog.md` | Orchestrator rules; backlog grammar rules |
| `output/{adhd,standard}/*.tmpl, *.golden`, `output/adhd.md` | Templates and golden fixtures per profile; the style text for the `AGENTS.md` block |
| `lenses/<lens>.json, <lens>.md` | Ten rule catalogs and bodies |
| `langs/<lang>.json` | Per-language definition patterns for the repo map |
| `skills/index.json`, `skills/<name>/SKILL.md` | Generated index with SHA-256 per skill; 5 workflow skills plus 34 `standards-*` |
| `defaults.json` | Built-in defaults and hard limits |
| `events.schema.json` | Generated at build from the event enum |
| `models.json` | Default models per weight and link, Claude windows and prices |
| `policy.json` | Guard rows with scopes, steering phrases |

### 6.4 Runtime state

```
<git-common-dir>/komodo/
├── install.json                               # manifest v1
├── runs/<run>.jsonl                           # run journal
├── runs/<group>/{events.jsonl, state.json}    # group journal, snapshot
├── jobs/state.json                            # job counters
├── sessions/<id>/{brief.md, request.json, stream.jsonl, messages.jsonl, result.json, out/}
├── ledger/{sessions.jsonl, events.jsonl, tasks.json, groups.json}
└── cache/{skills,context,checks,review,setup,map}/   # 256 MB LRU per repo

~/.komodo/                                     # %LOCALAPPDATA%\komodo on Windows
├── config.json                                # after the first `komodo config` write
├── release.key                                # ed25519 private key, mode 0600, owner's machine only
├── bin/komodo                                 # current version copy
├── versions/<v>/komodo                        # every installed version
├── jobs/*.json                                # job templates
├── leases/<repo-id>/<group>.{json,fence}      # lease table with fences
├── wt/<repo-id>/<group>/                      # worktrees
├── cache/lang/<repo-id>/                      # language caches per repo
└── state/{providers.json, quota.json, pool/, links/, queue/, wake/, fresh.json, fresh.lock, fresh.log, active/, config.log, config.sha}
```

---

## 7. Command-line interface

### 7.1 Processing pipeline

| # | Step | Rule |
|---|---|---|
| 1 | Parse | One verb table, stdlib `flag` per verb; `--input` JSON merges under explicit flags, which win |
| 2 | Validate | Types, enums, closest match for unknown flags, the terminal check, the repo pin, the config hash guard; nothing has run |
| 3 | Resolve | Repo id from cwd or `-C`, the effective config, the capacity profile when a verb needs a link |
| 4 | Dispatch | One thin call into the domain package; the verb never reads state files itself |
| 5 | Render | The result's codes and facts through templates into `report`, then the envelope or text |

A verb that fails in steps 1 to 3 has made 0 writes; `--dry-run` applies from step 4. Every writing verb is idempotent: a repeat with the same arguments reports `0 changes`.

### 7.2 Envelope and exit codes

| Field | Type | Meaning |
|---|---|---|
| `v` | int | Envelope version, 1 |
| `ok` | bool | Success |
| `verdict` | string | One line |
| `report` | string | Human text formatted to `output.profile`; relay verbatim |
| `data` | object | The verb's typed result |
| `error` | `{code, reason, fix}` | Set when `ok` is false; `code` is what a caller branches on |

| Exit | Meaning |
|---|---|
| 0 | ok |
| 1 | refused |
| 2 | usage (an unknown flag names the closest match) |
| 3 | blocked |
| 4 | internal |

### 7.3 Global flags

| Flag | Meaning | Default |
|---|---|---|
| `--json` | Print only the envelope | On when there is no terminal |
| `-C <path>` | Act on that repo | cwd |
| `--input <file\|->` | The verb's arguments as one JSON object | — |
| `-q`, `-v` | Less or more human text | — |
| `--no-color` | Plain text; `NO_COLOR` also works | auto |
| `-h`, `--help` | Same as `help <verb>` | — |
| `--dry-run` | Every writing verb: print each write, make none | — |

### 7.4 Verbs

| Verb | Does | Flags | Shared |
|---|---|---|---|
| `help [verb]` | List verbs, or one verb's flags and examples | `--all` | yes |
| `version` | Version, binary path, latest known, repo pin | `--check` | yes |
| `install` | Binary, provider probe, repo files | `--repo-only`, `--machine-only`, `--hosts claude,codex`, `--no-path`, `--from <file>`, `--project`, `--schedule`, `--force` | no |
| `uninstall` | Remove every file the manifest owns | `--repo-only`, `--machine-only` | no |
| `fresh` | Get the newest binary now | `--check`, `--to <version>`, `--restart-runs` | no |
| `doctor [install\|backlog]` | Health of the install or the backlog; both by default | `--fix` (terminal-only), `--sandbox <cmd>` | yes |
| `config [agent\|endpoint <name>] <key>[=<value>]…` | Read keys, or set several at once, all or none | `--all`, `--default`, `--accept` | reads only |
| `sync` | After a merge: fetch, prune worktrees, refresh repo files | `--no-fetch` | yes |
| `add backlog` | Add a task, group or epic | `--task`, `--group`, `--epic`, `--to <id>`, `--files <globs>`, `--done-when <text>` | yes |
| `add changelog` | Add an `Unreleased` entry | `--entry <text>`, `--kind added\|changed\|fixed\|removed` | yes |
| `plan <goal>` | Planner drafts groups; writes only if lint passes | `--from <spec>`, `--epic <N>` | yes |
| `run [group]` | Start groups detached, return the run id at once; refuses a group whose dependency has not landed | `--stage <stage>` | yes |
| `status [run]` | Pool, group graph, stages, budgets, blocks | `--watch`, `--wait blocked\|done\|any`, `--timeout <dur>` (9m), `--session <id>`, `--explain`, `--backlog`, `--metrics`, `--all` | yes |
| `skill list\|get` | Skill index and bodies | `list --agent <name>`, `get <name> --section <heading>` | yes |
| `scope approve\|deny <group>` | Answer a builder's `needs_scope`; approve adds the paths to `files` | `--paths <list>`, `--reason <text>` | yes |
| `resume <run>` | Continue a crashed, blocked, paused or restarted run; paused agents restart now | `--from <stage>` | yes |
| `stop <run>` | Save work and release leases; the same clean shutdown as a signal | — | yes |
| `abandon <run>` | Stop, free leases, keep evidence; dependents block with `dependency` | `--reason <text>`, `--keep-worktree` | yes |
| `jobs list\|show\|run\|pause\|resume\|tick` | Job templates and the tick | `<id>`; `run --ignore-window` | `list`, `show`, `tick` only |
| `cache stats\|clear` | Size, hit rate, purge | `--kind skills\|context\|checks\|review\|lang`, `--all` | `stats` only |
| `gate` | Full self-check; Komodo repo only | `--only <check>`, `--fast`, `--install`, `--install-hooks` | yes, except `--install*` |
| `release <version>` | Tag, cross-build, checksum, sign; Komodo repo only | `--notes-file <path>` | no |

Internal tier (`komodo internal <verb>`, listed only in `help --all`):

| Verb | Called by | Does |
|---|---|---|
| `internal tools --agent <a> --session <id>` | The engine, for `claude -p` | The private MCP server for one session, over stdio |
| `internal fresh-bg` | Any verb, only when a newer version is known | The detached updater: download, verify, swap |

### 7.5 Tier enforcement

- A terminal-only verb refuses with exit 1 unless it has a controlling terminal. Agent shells have none: Claude Code's Bash tool cannot open `/dev/tty`.
- No host hooks enforce tiers. Every host gets the same `AGENTS.md` rules and the same verbs. Rule O7 forbids faking a terminal; nothing enforces it.
- Pin check first: a repo whose `AGENTS.md` pin (`min=`) is above this binary refuses every verb except `help`, `version`, `fresh`, `install` and `doctor`, with the fix line `run komodo fresh`.

### 7.6 Performance

Short verbs (`help`, `version`, config reads, `skill`) answer under 50 ms with no network and no repo scan beyond marker stats. `help --json` and the `komodo` skill are generated at build and embedded. `[PENDING G-19]` the budget is a target until gate timing measures it in phase 6.

---

## 8. Configuration

### 8.1 Layers and write path

| # | Layer | Where | Power |
|---|---|---|---|
| 1 | Built-in | `embed/defaults.json`, `embed/policy.json` | Defaults and hard limits |
| 2 | Machine | `~/.komodo/config.json`, flat keys at top level, `agents.<name>` and `endpoints.<name>` sections | Any value within the hard limits |

- Merge: the machine value wins within the hard limits. Every key exists out of the box; `--default` resets a key, an agent, a group or the whole file (endpoint and whole-file resets list each change and need a typed `y`).
- Strict decode: an unknown key is an error naming the closest match; a value over a hard limit is refused naming the key, value and limit.
- Write: `komodo config k=v k2=v2` sets every pair or none; one call reads or writes, never both; writes need a terminal.
- Audit: each write appends one row per changed key to `~/.komodo/state/config.log`: time, batch id, key, old value, new value.
- Hash guard: `komodo config` writes the file and its SHA-256 to `state/config.sha`. A hand edit makes every verb refuse with `config_tampered` and the fix line `komodo config --accept`, which rewrites the hash and logs one audit row.
- Snapshot per run: the effective config hash goes into `runs/<group>/state.json`; the hash is also the cache key prefix, so a config change never serves a stale brief or check.
- Agents cannot touch it: `~/.komodo/**` is protected by G3 and every write needs a terminal.
- Repo id (names worktrees, leases and caches, never config): the normalized origin URL such as `github.com/rdevitto86/komodo-agentic-factory-coding`; with no remote, the git common dir path. Every worktree of a repo shares one id.

### 8.2 Flat keys (33)

| Key | Default | Hard limit | Meaning |
|---|---|---|---|
| `output.profile` | `adhd` | — | Template set: `adhd` or `standard`; facts and codes are the same under both |
| `output.turn_end_summary` | `true` | — | The 4 headings at the end of reports |
| `output.color` | `auto` | — | `auto`, `on`, `off` |
| `mode` | `thorough` | — | `economy` or `thorough`; sets review depth only |
| `fresh.auto` | `true` | — | Background update |
| `fresh.interval` | `1h` | ≥ 10m | How often to check |
| `providers.paid` | `never` | — | `never`, `fallback`, `allowed` |
| `warn.usd_per_run` | 5 | — | Warn when a run's metered USD passes this; never stops work |
| `warn.quota_pct` | 90 | — | Warn when a provider window passes this percent |
| `providers.keys.anthropic` | `env:ANTHROPIC_API_KEY` | `env:` only | Key reference, never the value |
| `providers.keys.openai` | `env:OPENAI_API_KEY` | `env:` only | Same |
| `providers.ctx_floor` | 262144 | — | Smallest `http:` endpoint context allowed |
| `http.ca_bundle` | none | PEM file path | Extra trusted certificates added to the system roots |
| `limits.pool_min` | 1 | `limits.pool_max` | Session slots kept open while throttled |
| `limits.pool_max` | 6 | 16 | Most looping sessions at once, across all repos |
| `limits.link_sessions.<link>` | 3; loopback endpoints 1 | 8 | Concurrent sessions per link, across all repos |
| `limits.group_wall_min` | tasks × `limits.session_wall_min` + 2 × `limits.session_wall_min` | 720 | Wall time per group; paused time excluded; a 4-task group gets 360 |
| `limits.group_tokens` | 2,000,000 | 10,000,000 | Tokens per group: input, output, cache writes; cache reads reported only |
| `limits.task_attempts` | 6 | 10 | Model attempts per task: fresh sessions, re-prompts, gate feedback, "continue" |
| `limits.pause_max_min` | 360 | 1,440 | Longest a session stays paused on a provider limit |
| `limits.session_wall_min` | 60 | 120 | Wall time per session |
| `limits.session_idle_min` | 10 | 30 | Silence before a nudge |
| `limits.repairs` | 3 | 5 | Returns to Build before Blocked |
| `guard.refusal_limit` | 3 | 5 | Refusals under one rule before the session ends |
| `check.patch_coverage` | 80 | 100 | Percent of changed lines a test must cover; blocks for Go, warns for other languages |
| `sandbox.network` | `deny` | — | Network for run-class commands |
| `sandbox.max_procs` | 512 | 2048 | Processes per session |
| `sandbox.max_mem_gb` | 8 | 32 | Memory per session |
| `sandbox.image` | detected | — | Docker image for native Windows; required for Makefile-only or 2-language repos |
| `sandbox.lang_cache_gb` | 10 | 50 | Language caches per repo |
| `cache.max_mb` | 256 | 4096 | Cache size per repo |
| `ledger.retention_days` | 30 | 365 | Ledger retention |
| `ship.mode` | `group` | — | `group`: a PR per group into the epic branch, merged by the engine; `epic`: groups merge straight into the epic branch |

Evidence-backed defaults: `limits.pool_max` 6 and `link_sessions` 3 are bounded by the Max 5-hour window, not the API; `providers.ctx_floor` 262,144 is the native window of the ~27B open models targeted and catches Ollama's 4,096 default; `limits.session_wall_min` 60 bounds one task with 6 attempts inside a 6-hour group; `limits.task_attempts` 6 covers 2 fresh sessions with 2 re-prompts each; `warn.usd_per_run` 5 warns before a second ~$7 group on Opus 5.5; `limits.group_tokens` 2,000,000 is 1.7× the expected worst case of 1.2M. `[PENDING G-07]` the real Max 5-hour window against `link_sessions.claude-cli` 3, measured over the first 20 groups.

### 8.3 Agent group (`komodo config agent <name> …`)

| Key | Default | Hard limit | Meaning |
|---|---|---|---|
| `model` | The agent's weight: `opus`, `sonnet`, `haiku` | A probed model | `opus` is the latest in the line, `opus-prev` the prior; `<endpoint>:<model>` names an open-source model; `fable` opt-in on heavy agents |
| `session_wall_min` | `limits.session_wall_min` | 120 | Beats the flat key for this agent |
| `session_idle_min` | `limits.session_idle_min` | 30 | Same |
| `retries` | 2 | 3 | Retries per session |

### 8.4 Endpoint group (`komodo config endpoint <name> …`)

| Key | Meaning |
|---|---|
| `url` | The server's `/v1` base |
| `models` | A model per weight: `heavy`, `standard`, `light`; a missing weight uses the nearest |
| `ctx` | Context tokens: required for non-Ollama servers; an optional lower cap for Ollama |
| `key` | `env:` reference only, optional |
| `self_hosted` | A non-loopback server counted as free |
| `first_byte_s` | First-byte timeout override |

Default: one endpoint, `local`, at `http://localhost:11434/v1`, used only when it answers the probe.

### 8.5 Environment variables

| Variable | Use |
|---|---|
| `KOMODO_HOME` | Another home dir (tests, CI) |
| `NO_COLOR` | Plain text |

No other environment variable configures Komodo. `CI=true` disables background fresh.

### 8.6 Command detection

Cached by the hash of the marker files.

| Marker | setup | build | test | lint | format |
|---|---|---|---|---|---|
| `go.mod` | `go mod download` | `go build ./...` | `go test ./...` | `go vet ./...` | `gofmt -l .` |
| `package.json` | `npm ci` | `npm run build` | `npm test` | `npm run lint` | `npm run format` |
| `Cargo.toml` | `cargo fetch` | `cargo build` | `cargo test` | `cargo clippy` | `cargo fmt --check` |
| `pyproject.toml` | — | — | `pytest` | `ruff check` | `ruff format --check` |
| `Makefile` | `make setup` | `make build` | `make test` | `make lint` | `make fmt` |

- Only what exists: npm scripts and make targets are used only if defined; `npm ci` needs a lockfile.
- A Makefile target wins over the detected command when the Makefile defines it.
- `git-read` is always on: `git status`, `diff`, `log`, `show`.
- Commands run without a shell; shell syntax fails `doctor`. Every command runs in the sandbox, except `git-read` under Docker, which runs on the host.
- `setup` is the engine's: Intake runs it once per worktree with network on; no agent can run it.
- With no detected commands, `run` refuses and names the marker files it looked for; read-only agents still run.

### 8.7 Capacity profile per link

Before each session the engine derives `{ctx, slots, cost_class, effort}` for the link it is about to use: `ctx` measured (§16.4), `slots` from `limits.link_sessions.<link>`, `cost_class` from the billing check, `effort` from the agent's weight. The brief builder sizes from `ctx`, the engine takes a slot from `slots`, and budgets cache by the whole profile. Swapping Claude for a local or OpenAI link changes the profile, never the engine.

---

## 9. Event model

### 9.1 Envelope

| Field | Type | Meaning |
|---|---|---|
| `v` | int | Envelope version, 1 |
| `seq` | int | Order within the group journal, no gaps |
| `ts` | RFC 3339 | When the engine saw the fact |
| `run`, `group`, `session` | string | Where it happened; `session` only on session and task events |
| `epoch`, `stale` | int, bool | The stage epoch the producer was spawned under; `stale` set by `Drive` when older than the current |
| `type`, `reason`, `data` | string, string, object | What happened; `reason` from the code list; `data` typed per event |
| `crc` | uint32 | CRC-32 of the line before it; replay drops a torn last line and emits `journal.truncated` once |

### 9.2 Catalog (32 types)

| Family | Types | Emitted by |
|---|---|---|
| Run | `run.start`, `run.done`, `run.stopped` | Engine; `run.stopped` by shutdown |
| Group | `group.paused`, `group.blocked`, `group.cancelled`, `group.shipped`, `lease.lost` | Engine, workspace, ship |
| Stage | `stage.enter`, `stage.exit` | Engine |
| Session | `session.start`, `session.idle`, `session.timewarn`, `session.end` (with exit reason) | Supervisor; provider supplies limit reasons |
| Work | `task.claimed`, `task.released`, `task.done`, `task.retry`, `check.done`, `finding.submit`, `finding.verified`, `tool.denied`, `timer.due`, `job.due`, `job.fail` | Tools, guard, supervisor, scheduler, recovery, Check, review |
| Ledger | `lock.taken`, `lock.released`, `action.start`, `action.done`, `journal.truncated` | `Drive`, recovery |
| Provider | `provider.skip` (with reason such as `ctx_below_floor`), `limit.warn` | Provider adapters |

Each type's `data` is a Go struct. The build generates `embed/events.schema.json` from the enum; `help --json` serves it as `events`; the gate fails when the two disagree.

### 9.3 Journals and snapshots

| Item | Rule |
|---|---|
| Group journal | `runs/<group>/events.jsonl`; one writer; append, fsync, then apply |
| Run journal | `runs/<run>.jsonl` for `run.*` events |
| Snapshot | `runs/<group>/state.json` holds the last `seq`; written atomically every 50 events or on a stage change |
| Replay | From the snapshot, never from `seq` 0; cost bounded by 50 events; a 10,000-event replay must finish under 1 s `[PENDING G-19]` |
| Torn line | CRC mismatch on the last line drops it and emits `journal.truncated` once |

### 9.4 The outbox

`Next()` returns actions; `Drive` appends `action.start {id, kind}` before running one and `action.done` after. `id = sha256(seq, kind, args)`. On replay a done action is skipped and a started one reruns. The gate fails an action kind missing from this table.

| Action | Idempotency key | Safe to repeat because |
|---|---|---|
| `spawn` | Session UUID chosen by Komodo | A live UUID is a no-op; a dead one restarts the session |
| `wip_commit`, `commit` | `(group, fence, tree hash)` | The hidden ref moves by compare-and-swap; an identical tree writes nothing |
| `cancel` | Session UUID | Killing a dead process is a no-op |
| `setup` | `(group, lockfile hash)` | Reruns are cached and the sandbox makes them pure |
| `check` | `(tree hash, command)` | Results are cached by that key |
| `land` | `(group, fence, merge base)` | A landed merge commit already exists; the fence refuses a stale lander |
| `open_pr` | `(epic, head SHA)` | `gh` finds the existing PR for that head first |
| `scope_change` | `(group, sorted paths)` | Adding a path twice is one path |
| `notify` | Event `seq` | A wake is a file touch |

### 9.5 Epochs

Every spawn carries the stage's `epoch`, an integer that rises on each stage entry. A session event whose epoch is older than the current stage's is journaled with `stale: true` and `Next()` ignores it. Returns to Build raise the epoch, so a cancelled Verify session's late result cannot move the stage twice.

---

## 10. Group state machine

### 10.1 Transitions

| From | Event | To | Condition |
|---|---|---|---|
| — | `run.start` | Intake | |
| Intake | `stage.exit` | Build | Lint pass, lease and fence taken, `commands.setup` exit 0, task list locked |
| Build | `task.done` | Check | The last task's `done_when` exit 0 and a commit on the hidden ref |
| Check | `check.done` pass | Verify | |
| Check | `check.done` fail | Build | Repairs under `limits.repairs` |
| Verify | `finding.verified` | Build | A verified blocking finding, a `not_met` task, or an item that failed Go's two reruns |
| Verify | `stage.exit` | Ship | Reviewer `met` for every task and 0 blocking findings; tester `confirmed` for every task and item; 0 regressions |
| Ship | `group.shipped` | end | Merge clean, Check on the epic tip, landing with the fence |
| Build or Verify | `session.end` reason `paused` | Paused | No allowed link left |
| Paused | `timer.due` | Build or Verify | The provider's reset time passed |
| Paused | `pause_timeout` | Blocked | `limits.pause_max_min` reached; WIP commit kept |
| Build or Verify | A bound is hit | Blocked | See §26 |
| Blocked | `resume` | Build | A person or the orchestrator |

A table test enumerates every (stage, event) pair, including stale ones; the four Verify orderings each move the stage exactly once.

### 10.2 Paused versus Blocked

| State | Waits on | Pool slot | Lease and worktree | Wall clocks | Ends by |
|---|---|---|---|---|---|
| Paused | A ledger timer (`wait_until`) | Returned | Kept | Stopped | The timer, `resume`, or `pause_max_min` |
| Blocked | A person or the orchestrator | Returned | Kept (sweep skips `needs_scope`) | Stopped | `resume`, `scope approve|deny`, `abandon` |

### 10.3 Block reasons

A fixed enum, named in `status` and the `group.blocked` event: `no_provider`, `pause_timeout`, `paid_refused`, `regression`, `epic_gate`, `tester`, `flaky`, `dependency`, `integration`, `needs_scope`, `attempts`, `context`, `merge_conflict`, `budget`, `crash`, `lint`, `brief`, plus the unnamed Blocked of exhausted retries, repairs, flaps and job backoff. A group blocked 3 times for one reason is flagged `needs redesign` in `status`.

---

## 11. Orchestration engine internals

### 11.1 Concurrency model

| Element | Rule |
|---|---|
| `Drive` per group | One goroutine owns the group's journal and state; nothing else writes them; no mutex inside a group |
| Session goroutine | One per session, producing events into its group's `Drive` channel, buffered to 256, never dropped: a full channel blocks the producer |
| Cross-group state | Files with OS atomicity under `~/.komodo/state/`: queue, pool and link slots, leases, `quota.json`; create-only or renamed into place |
| Run goroutine | Builds the DAG, enqueues ready groups, reaps finished `Drive`s; holds no group state |
| Panic containment | Every goroutine starts through one panic-catching helper; a panic in one `Drive` emits `group.blocked crash` for that group alone |

### 11.2 Work queue

- Path: `~/.komodo/state/queue/<repo-id>.<kind>.<id>.json`, one create-only file per ready group or due job.
- Shape: `{kind: group|job, repo, id, priority, enqueued_at}`.
- `run` enqueues groups as their dependencies land; `jobs tick` enqueues due jobs.
- Taking a pool slot deletes the file and writes the lease, in that order. A crash between the two leaves an item the sweep re-enqueues, never a lost one.
- Pick order: `priority: high` first; fair share across repos (round-robin when 2 or more repos have ready items; each job counts as its own repo); longest remaining chain weighted by task count; most groups unblocked; backlog ID order.

### 11.3 Session pool

| Rule | Detail |
|---|---|
| Slot takers | Builder, reviewer, tester (`pool_slot: true`); planner and job sessions take none but count against link limits |
| Floor, ceiling | `limits.pool_min` 1, `limits.pool_max` 6, hard limit 16 |
| Grow | By 1 after each clean session end, while free memory covers one more `sandbox.max_mem_gb` |
| Shrink | Halve, never below the floor, on a rate-limit error or quota past `warn.quota_pct` |
| Link slot | `limits.link_sessions.<link>` (3; loopback 1; hard limit 8). `[PENDING G-04]` whether `OLLAMA_NUM_PARALLEL` above 1 lets `local` exceed 1 |
| Files | Pool and link slots are lock files freed when their pid dies |
| No idle worktrees | Intake runs just before a group's first session takes a slot; the next ready group's Intake starts while the current one's first session loads |

### 11.4 Run order and convergence

| Rule | Detail |
|---|---|
| DAG | Built from the backlog; cycles refused; waves and longest chain computed |
| Start | After every dependency has landed and Check passed on the epic tip |
| Join | Two or more dependencies: start after all land |
| Named run | `run <group>` refuses when a dependency outside the run has not landed, naming it |
| Stuck dependency | Dependents show `waiting on <group>`; unrelated groups keep running |
| Abandoned dependency | Dependents block with `dependency` |
| Landing | One at a time; then Check on the epic tip (0 tokens) |
| Integration break | Check fails on the tip: landings pause; the last-landed group returns to Build in a new worktree cut from the tip; bound `limits.repairs`; then Blocked `integration` |
| Epic gate | After the last landing: reviewer and tester once over the whole epic diff; a finding goes to the group that last changed its file; a finding with no file blocks the epic with `epic_gate` |
| Inside a group | Tasks run one after another in the group worktree; one builder per group; independent tasks belong in separate groups |

### 11.5 Run flags

`--stage <stage>` runs one stage. There is no `--mode`, `--set`, `--max-parallel`, `--budget-tokens`, `--detach` or `--until`; `run` always detaches, and limits come only from `komodo config`.

---

## 12. Workspace and leases

### 12.1 Worktrees

| Item | Rule |
|---|---|
| Location | `~/.komodo/wt/<repo-id>/<group>/`, detached, shared by every clone; no path contains `.git` |
| Cut | `git worktree add --detach` from the epic branch tip, about 1 s |
| Tester worktree | A throwaway one per Verify at the same commit, dependencies copied per §22.5, removed when Verify ends; reuses the builder's object store |
| Windows | Names are short hashes to stay under `MAX_PATH`; each worktree sets `core.longpaths`, `core.filemode false`, `core.autocrlf false`; `doctor` warns when the long-path switch is off |
| Idle | A worktree unused for 3 days is WIP-committed to its hidden ref and removed; `resume` rebuilds it |

### 12.2 Lease

| Field or rule | Value |
|---|---|
| File | `~/.komodo/leases/<repo-id>/<group>.json`, create-only; the OS lets exactly one run create it |
| Contents | Run ID, pid, host, clone path, `fence`, taken and renewed times |
| Held | From Intake until landing or abandon, through queued, checking, verifying, paused and blocked |
| Renewal | Every 5 min by the group's `Drive` |
| Expiry | 2 h after the last renewal |
| Takeover | A stale or dead-holder lease is renamed aside (`.stale-*`) before takeover, so only one run wins |
| Release | On landing or abandon; on clean shutdown after work is saved; by the parent guard after a crash; by the startup sweep when both died. Never while agents live or work is unsaved |
| Protection | Verbs refuse a leased worktree; rule O4 forbids touching one by hand |

### 12.3 Fence

- `<group>.fence` holds a monotonic integer incremented on every take or takeover.
- Every side effect carries it: the hidden-ref update, the push, the landing, a scope change.
- The resource enforces it: `git update-ref <ref> <new> <old>` is a compare-and-swap that exits 128 (`cannot lock ref`) when the ref moved; the reflog message records `fence=<n>`.
- A holder whose update is refused emits `lease.lost`, and `Next()` stops the group.

### 12.4 Saved work

The hidden ref `refs/komodo/groups/<group>` moves to every group commit by compare-and-swap with the fence in its reflog. Git keeps it, `git branch` hides it, it is never pushed, and it is deleted when the group lands or is abandoned.

### 12.5 Shutdown and the parent guard

| Step | Rule |
|---|---|
| Sequence | Stop agents (30 s each), WIP-commit each worktree, free leases, write `state.json`, exit; after 60 s the rest is force-killed |
| Trigger | Any signal, `stop <run>`, or `fresh --restart-runs` |
| Bus | Shutdown is direct code outside the bus; it writes `run.stopped` when it can |
| Parent guard | `komodo run` starts a small parent that starts the engine. If the engine dies past its handler (`kill -9`, OOM, Go fatal) or writes no heartbeat for 10 min, the parent kills its agents by pid file and runs the same save and release. Power loss is left to the sweep |

### 12.6 Startup sweep

Kills orphans by pid file; frees dead leases; deletes `.stale-*` lease files and leases whose clone path is gone; removes dead runs' tester worktrees, Docker containers and images, session temp, and worktree folders whose clone is gone; removes worktrees with no live lease and no unsaved work; re-enqueues queue items with no lease; emits `session.end crash` for dead sessions; deletes a repo's language cache when over `sandbox.lang_cache_gb` or unused 30 days, never while the repo holds a lease. Four scans run in parallel and must finish under 2 s with 20 dead runs.

---

## 13. Session supervisor

### 13.1 Lifecycle

| Phase | Rule |
|---|---|
| Spawn | Start the provider session with its brief, tool server and sandbox profile; record `request.json`; the brief is written once to `<session>/brief.md` and piped from there |
| Heartbeat | Any stream line or tool call counts; silence starts the idle clock. Liveness is events, never pings: alive while the stream produces lines, a tool call runs, or an `api_retry` line arrived within its `retry_delay_ms`. On API links the HTTP stream-idle timer is the heartbeat |
| Timer | One 15 s timer per session checks wall, idle and the 80% warning; a nudge or kill lands at most 15 s late |
| Time warning | At 80% of wall, one fixed line injected: "Finish the current task and stop; 12 minutes remain" (the number is computed) |
| Nudge | After `session_idle_min` with no stream line and no running tool: one fixed line, "Report status or stop now; the harness will ask for your result"; 5-min grace; then stop. `[PENDING G-14]` a nudge reaching a turn stuck inside a tool call is best effort |
| Injection | Through `claude -p --input-format stream-json` (stdin stays open) or the next API turn |
| Stop ladder | SIGINT (ends the turn, keeps its result); after 30 s SIGTERM; after 60 s a whole-tree kill. API links: context cancel after the current tool call returns |
| Orphans | Process groups on Unix, Job Objects on Windows |
| Paused clock | Wall clocks stop while the group is paused |

### 13.2 Controls

| Control | Value |
|---|---|
| Wall | `limits.session_wall_min` 60, max 120; warning at 80% |
| Idle | 10 min; one nudge, 5-min grace, then stop |
| Retries | Per agent from `agent.json` (2, max 3): timeout or idle restarts the task's session, inside `limits.task_attempts` |

### 13.3 Exit reasons

`done`, `needs_scope`, `check_fail`, `no_result`, `schema_fail`, `timeout`, `idle`, `refusals`, `budget`, `provider`, `billing`, `paused`, `pause_timeout`, `cancelled`, `crash`. `result.json` records the reason. A session halted by a provider limit with a resume handle exits `paused`, not `provider`.

### 13.4 Resume

`resume` tries the provider's handle first: `--resume <uuid>` with `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1` on `claude-cli`, or the saved `messages.jsonl` on API links. A failed resume gets a fresh session with the brief, the WIP diff and the last builder `summary`, and the ledger logs `resume_lost`.

---

## 14. Agent registry

### 14.1 `agent.json` (what Go reads)

| Field | Type | Rule |
|---|---|---|
| `v` | int | Schema version, 1; an older file fails the gate |
| `weight` | `heavy\|standard\|light` | Picks the model per link and the API `effort`: `high`, `medium`, `low` |
| `chain` | `[]string` | Links in order; must end in a free `http:` endpoint |
| `tools`, `run_classes` | `[]string` | The only tools advertised (G1) and the only `run` classes (R1) |
| `guard` | `[]string` | Row IDs from `policy.json` beyond the inherited ones |
| `limits` | `{wall_min, idle_min, retries}` | Time and retries only; no turn or token caps |
| `skills` | `{allow, always}` | Skill allowlist and preinjects |
| `slots` | `{required, optional}` | Brief slots |
| `result` | JSON Schema | The final call's schema; `code` fields are enums from the code list |
| `pool_slot` | bool | True for builder, reviewer, tester |

Strict decode (`DisallowUnknownFields`). The registry decodes the 7 agents once per process and checks: no two agents share a `result` `$id`; every `guard` row exists in `policy.json`; every tool in `tools` is registered. A verb or the engine asks the registry by name and never re-reads `embed/`.

### 14.2 `prompt.md` (what the model reads)

`# Role` text, never a rule, then a `# Brief` template with `{{slots}}`. Slots fill in one pass, so a filled value is never expanded again. The gate fails an unfilled or unknown slot.

### 14.3 Roster and limits

| Agent | Weight |
|---|---|
| Planner, architect, reviewer | heavy |
| Builder, researcher | standard |
| Tester, scout | light |

| Limit | Default | Hard limit |
|---|---|---|
| Wall | 60 min | 120 min |
| Idle | 10 min | 30 min |
| Retries | 2 per session | 3 |

The chain is fixed in `agent.json`, never in config; `komodo config agent <name>` may change the model and limits only. Tool definitions and role text are the first two prefix parts, identical for every session of an agent.

---

## 15. Tool runtime

### 15.1 Registry and transports

- The registry binds the tools in `agent.json` to one session; unlisted tools are not advertised (G1).
- `claude-cli`: a private MCP server, `komodo internal tools --agent --session`, over stdio, started with `--strict-mcp-config`; it dies with its session and starts in under 100 ms because it reads nothing from disk.
- API links: Go runs the harness tool loop, calling the same tool functions.

### 15.2 Harness tool loop (API links)

1. Tools stay on `auto`; the model reasons and calls tools freely; thinking stays on.
2. A text-only reply ends the work. No nudge.
3. One final call returns only the agent's result schema (per link, §16.3).
4. The engine checks the result: schema first (1 re-prompt), then the agent's own gate (`done_when` for a builder, `lint_backlog` for a planner). A failure goes back into the same session with its output; 2 rejected results end it with `check_fail`.
5. A reply cut off at the output limit gets "continue" once, then ends with `no_result`.

Context limit: Go counts tokens every turn against the link's measured context. At 80%, old tool outputs shrink to one-line stubs; their full text stays in `<session>/out/`. Still over, the session ends with `no_result`; nothing past the context is sent.

### 15.3 Tools and caps

| Tool | Rule |
|---|---|
| `read` | 2,000 lines per call, line ranges; repeat read of an unchanged file returns "unchanged since your last read (N lines)"; a second read in a row returns full content |
| `search` | Go walker with `.gitignore` applied and a 1 MB per-file cap; never scans `node_modules` or `target/` |
| `edit` | Exact, unique search-and-replace; a miss returns the closest lines; the reply shows the edited lines after `tool.post` formats them |
| `write` | New files only; an existing file is refused with "use `edit`" |
| `run` | `{class, args[]}`; Go runs the class's command plus args directly, no shell; inside the sandbox; 10-min timeout |
| `diff`, `git_read` | Reviewer only; read-only git |
| `skill` | Any allowlisted skill or `<name>#<section>` |
| `submit_finding` | Rule ID, `file:line`, quoted evidence; unknown rule ID refused |
| `lint_backlog` | Planner only |
| `web` | Architect and researcher, opt-in, GET only, allowlist |
| External MCPs | Opt-in allowlist per agent, no credentials (final phase) |

Output cap: 4 KB for every tool except `read`, head and tail kept; the full output goes to `<session>/out/<call>.log` and the capped reply names the path.

### 15.4 Per-agent tool matrix

| Agent | Tools | Run classes |
|---|---|---|
| Builder | `read`, `search`, `edit`, `write`, `run`, `skill` | declared classes |
| Reviewer | `read`, `search`, `skill`, `diff`, `git_read`, `run`, `submit_finding` | build, test, lint |
| Tester | `read`, `search`, `run` | build, test |
| Planner | `read`, `search`, `skill`, `lint_backlog` | — |
| Architect | `read`, `search`, `skill`, `web` | — |
| Researcher | `read`, `search`, `skill`, `web` | — |
| Scout | `read`, `search` (paths only) | — |

### 15.5 Host facts the server is built against (Claude Code 2.1.295)

A stdio MCP server has no per-request timer and `MCP_TOOL_TIMEOUT` defaults to about 28 hours, so the sandbox's 10-minute `run` timeout is the only one that fires. `MAX_MCP_OUTPUT_TOKENS` defaults to 25,000 and the host warns past 10,000, so the 4 KB cap stays far below both. The server must answer `initialize` inside `MCP_TIMEOUT` (30 s). The worker env never sets `CLAUDE_CODE_RETRY_WATCHDOG`; instead the supervisor reads `system/api_retry` lines, and on `rate_limit` or `billing_error` the engine ends the session with reason `provider`, keeps its resume handle, and the provider layer fails over or pauses.

---

## 16. Provider adapters

### 16.1 Interface

`Probe`, `Start(session) → stream`, `Resume(session, input)`. The stream ends in a `Result`: the output, usage (tokens in, out, cache read and write, plus USD) and the resume handle.

### 16.2 Links

| Link | Runs as | Auth | Tools via | Beta 6 |
|---|---|---|---|---|
| `claude-cli` | `claude -p` at the probed absolute path | The CLI's login | Private MCP server | ships |
| `anthropic-api` | HTTPS | `providers.keys.anthropic` | Go tool loop | ships |
| `http:<name>` | OpenAI-compatible HTTP | Optional `key` | Go tool loop | ships |
| `codex-cli` | `codex exec` | The CLI's login | Private MCP server | `not yet` `[PENDING G-20]` |
| `openai-api` | HTTPS | `providers.keys.openai` | Go tool loop | `not yet` `[PENDING G-20]` |

### 16.3 Schema enforcement per link

| Link | Final call |
|---|---|
| `claude-cli` | `--json-schema` |
| `codex-cli` | `--output-schema` |
| `anthropic-api` | Structured outputs (`output_config.format`) with thinking left on; Opus 5.5 and Sonnet 5.5 refuse a forced `tool_choice` with a 400 |
| `openai-api` | No tools, `response_format: json_schema` with `strict: true` |
| Ollama and other `http:` | No tools, the schema as `response_format`; Ollama has no `tool_choice` |

Every link ends the same way: tools on `auto`, a text-only reply ends the work, one final call returns the result schema, Go re-validates.

### 16.4 Measured context

| Link | Source |
|---|---|
| `claude-cli`, `anthropic-api` | Models API `max_input_tokens`, else the `embed/models.json` entry, else the default window 262,144 |
| Ollama | `/api/ps` `context_length` for the loaded model; an unloaded model is loaded with an empty request first; a typed `ctx` only lowers it |
| Other `http:` servers | The endpoint's `ctx`, required |

Each session's measured context and its source travel in `session.start`. Ollama is detected at the probe by `GET /api/version`. An endpoint below `providers.ctx_floor` fails `doctor` and is skipped with `provider.skip` reason `ctx_below_floor`; `doctor` names the fix `OLLAMA_CONTEXT_LENGTH=262144`. `[PENDING G-03]` a 24 GB machine running Qwen3.6-27B at 262,144 context.

### 16.5 Model catalog (`embed/models.json`, about 10 entries)

| Weight | Latest | Prior | Window | Price per M in/out |
|---|---|---|---|---|
| Heavy | `claude-opus-5-5` | `claude-opus-5` | 1M, 128K out | $4 / $20 |
| Standard | `claude-sonnet-5-5` | `claude-sonnet-5` | 1M, 128K out | $2 / $10 |
| Light | `claude-haiku-5-5` | `claude-haiku-4-5` | 1M; Haiku 4.5 200K | $0.10 / $0.50 |
| Heavy, opt-in | `fable` (Fable 5.1) | — | 1M | $10 / $50 |
| Local | Qwen3.6-27B, Gemma 4 31B, Nemotron 3 Nano per weight | — | measured | free |

IDs come from the Models API `line` field when an Anthropic key resolves, else the catalog. The probe refreshes them on `install`, `doctor` and `fresh` and writes exact IDs to `providers.json`, so a run never changes model mid-way. A line with no current model moves its weight up one line and `doctor` warns. `doctor` prints the catalog with each window's source. `[PENDING G-06]` whether a Max login exposes `claude-fable-5-1` to `claude -p`.

### 16.6 Failover chain

| Step | Link | Model per weight |
|---|---|---|
| 1 | `claude-cli` | Latest ID, then the prior ID |
| 2 (backstop) | Each free `http:<name>` in config order | The endpoint's `heavy`, `standard`, `light`; a missing weight uses the nearest |
| 3 | `anthropic-api` | Only under `providers.paid: fallback` or `allowed` |

- Skip only when unavailable, unauthorized or over a limit; never because a task failed. Each skip: `provider.skip {from, to, reason}` plus a warning naming the model used.
- A model retired mid-run: a 404 fails that session start; the task's next session takes the prior link, then the backstop (G-05, decided).
- Any Claude limit (5-hour window, weekly cap, model cap): the next session starts on the backstop. After the recorded reset time, new sessions return to `claude-cli`; a task already on the backstop finishes there.
- The backstop is mandatory; `doctor` and `gate` refuse a chain without one; `install` warns when no endpoint answers.
- An agent set to a `-prev` model falls back to the backstop, never forward to the latest.

### 16.7 Billing check (fails closed)

| Cost class | When | Runs |
|---|---|---|
| `subscription` | `claude-cli` with `authMethod: claude.ai` and `subscriptionType` in `pro`, `max`, `team`, `enterprise` | always |
| `free` | `http:` on loopback, or `self_hosted: true` | always |
| `paid` | `anthropic-api`, `openai-api`, `claude-cli` on any other login, any other `http:` | per `providers.paid` |

| `providers.paid` | Effect |
|---|---|
| `never` | Paid links removed from every chain; the run blocks with `paid_refused` naming the key |
| `fallback` | A paid link runs only after every subscription and free link is skipped |
| `allowed` | Paid links run in chain order |

1. Before each `claude-cli` session: `claude auth status --json` (0.1 s, cached 60 s per process) sets the cost class; a logged-out or unknown login is `paid`.
2. Env scrub: `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_*` removed; `CLAUDE_CODE_OAUTH_TOKEN` stays.
3. First stream line: `system/init` must say `apiKeySource: "none"`, else the session is killed before turn 1 with exit `billing`. The probe records one init line per CLI version; a version without the field fails the link at probe time. `[PENDING G-02]` capturing it without a model turn.

### 16.8 Limits, pauses and warnings

| Rule | Detail |
|---|---|
| Provider halt | Next session on the next allowed link; only with no link left does the session pause with its resume handle |
| Timer on disk | The ledger row gets `wait_until` (reset plus 1 min) and `wait_link`; the link's reset time goes to `~/.komodo/state/quota.json` so every repo skips it |
| Who fires it | The live engine sleeps until the earliest `wait_until`; else the startup sweep, `jobs tick` or `status` finds it |
| `resume` | Attempts every paused agent now from its handle; a refused attempt costs no turn and re-pauses with the new reset, or backs off 15, 30, then 60 min |
| Cap | `limits.pause_max_min` 360: the session is cancelled, WIP commit kept, group Blocked `pause_timeout` |
| Visible | `status` shows `paused: provider_limit · claude-cli · resumes 15:31 (in 2h 14m)`; `abandon` clears the timer |
| Warnings, never stops | Window past `warn.quota_pct`; `rate_limit_event` with `isUsingOverage: true`; run USD past `warn.usd_per_run`. Shown under ⚠️ Warnings in the run report, `status`, `status --wait`, and as `limit.warn` |
| USD | Metered links ledger `usd`; subscription sessions ledger `total_cost_usd` as notional `usd_equiv` |

### 16.9 `claude-cli` launch line

```
<probed path> -p --output-format stream-json --input-format stream-json --verbose
  --session-id <uuid>
  --model <weight → model>
  --system-prompt-file <session>/system.md
  --tools ""
  --mcp-config <session>/mcp.json --strict-mcp-config
  --permission-mode dontAsk
  --permission-prompts none
  --allowedTools mcp__komodo
  --setting-sources ""
  --json-schema <agent result schema>
  < brief as one stream-json user message; stdin stays open, closed after the result
```

Resume: `--resume <uuid>` with the repair input on stdin. No `--bare`: it never uses the Max login. `--setting-sources ""` loads no settings, hooks or `CLAUDE.md` (tested on 2.1.291: 0 hooks fired, a planted `CLAUDE.md` ignored). Stream facts: the first line is `system/init`; `system/api_retry` lines carry error categories (`rate_limit`, `overloaded`, `billing_error`, `max_output_tokens`); piped stdin is capped at 10 MB and a 500K-token brief is about 2 MB; MCP servers get `MCP_TIMEOUT` 30 s; SIGINT ends a turn with its result, SIGTERM leaves it unfinished with exit 143.

Finding `claude`: `exec.LookPath("claude")` first (finds `claude.exe` on Windows), then `~/.local/bin/claude`, `%USERPROFILE%\.local\bin\claude.exe`, `/opt/homebrew/bin/claude`, `/usr/local/bin/claude`. Never a file under `~/.local/share/claude/versions/`. Never read credentials: login state comes only from `claude auth status --json`. WSL and Windows probe their own side.

Effort per weight on API links: heavy `high`, standard `medium`, light `low`, fixed per agent, because an effort or thinking change invalidates the prompt cache. Ollama `keep_alive` is set to the run's remaining wall time.

---

## 17. HTTP stack

| Value | Setting |
|---|---|
| Client | One `http.Transport` per process, keep-alive, HTTP/2 to hosted links; system TLS roots plus `http.ca_bundle`; `HTTPS_PROXY`/`NO_PROXY`; no cookies; loopback never proxied |
| Connect / first byte | 10 s / 60 s; loopback 300 s; per endpoint `first_byte_s` |
| Stream idle | 90 s hosted; 300 s loopback and `self_hosted` |
| Stream drop mid-answer | 1 retry from the start, partial discarded; a second failure fails the session |
| Retry classes | Connection reset, 408, 429 with `retry-after` ≤ 60 s, 500, 502, 503, 504, Anthropic 529 `overloaded` |
| Returned as a limit, not retried | 429 with `error.details.error_code: enforced_spend_limit_reached` and no `retry-after`; 429 with `retry-after` over 60 s; a quota error in the body |
| Retries | 4 attempts, full jitter: sleep = random(0, min(30 s, 1 s × 2^attempt)); `retry-after` replaces the formula |
| Headroom | `anthropic-ratelimit-*-remaining` and `-reset` read on every response; below `100 − warn.quota_pct` percent remaining, a `limit.warn` fires before any 429 |
| Plain `http://` | Loopback, or `self_hosted` with a ⚠️ warning; anything else needs HTTPS |
| Custom CA | A PEM file added to the system roots; missing or bad fails `doctor` |
| Body cap (non-stream) | 8 MB |
| Redirects | Same host only |
| Host allowlist per caller | Providers reach their endpoint; `web` only the allowlist and GET; the updater only the release host |
| Redaction | API keys and every `env:` value never reach logs, ledger rows or error text |

Users: `anthropic.go`, `openai.go`, the `web` tool, `internal fresh-bg`. `claude -p` and `codex` own their own networking.

---

## 18. Skill loader

| Rule | Detail |
|---|---|
| Index | Keyed on name, file globs and repo markers; covers 34 `standards-*` plus `komodo`, `plan`, `run`, `respond`, `release`; embedded at build |
| Source | `embed/skills/` only; the repo's `.claude/` is never read |
| Allowlist | An agent's `skills.allow` is the only set it can be given or pull; `always` names preinjects (builder and reviewer: `standards-comments`) |
| Preinject | A skill loads when its globs match a file in the group's `files` and its markers exist; no count or token cap |
| Pull | The `skill` tool and `komodo skill get` return any allowlisted skill or `<name>#<section>`; bodies from the cache; no pull limit |
| Markers | `standards-ui-mobile` needs `AndroidManifest.xml` or `*.xcodeproj`; `standards-aws` needs `cdk.json`, `samconfig.toml` or an AWS provider in `*.tf`; `standards-cicd` drops `**/Makefile` |
| Order | `always` first, then name order, so the prefix is byte-identical across runs |
| Too big | A brief over the ceiling never drops a skill; the task goes to the next link, else Blocked `context` |
| Frontmatter | Strict, `v: 1`: `name`, `description`, `globs`, `markers`, `sections`; the gate fails an unknown key, duplicate heading, or a `sections` entry with no heading |
| Size | 6,000 tokens per skill; the gate names any over it; skill tokens per session go to the ledger |
| Integrity | `embed/skills/index.json` has a SHA-256 per skill; verified on read; each renders under `## Standards: <name> (<sha8>)` |
| Steering scan | The gate fails a skill, and `doctor` warns on a repo addendum, containing a `text`-row phrase: `ignore previous`, `you are now`, `system prompt`, `disregard`, `komodo config`, `komodo install`. The addendum still loads with the warning. `[PENDING G-13]` the list is 6 entries by judgment |
| Standards to rules | `- **Never` and `- **Always` lines compile to reviewer rule IDs `std.<skill>.<n>`; an edit without the regenerated list fails the gate |

---

## 19. Rules and output contract

### 19.1 Where rules are enforced

| Rule target | Enforcer |
|---|---|
| Tool calls | The guard; a refusal names the rule and the allowed alternative |
| Results | The result schema; each field's description states the allowed outcomes |
| Work | Check and review |
| Comments | The `standards-comments` skill, preinjected for builder and reviewer |
| The orchestrator | O1 to O7, O9, O10 as text in the `AGENTS.md` block, rendered from `embed/rules/orchestrator.md` |

No rule text enters any worker brief. Role text in `prompt.md` is not a rule.

### 19.2 Output mapping (`embed/output/`)

| What a person reads | Facts from | Template |
|---|---|---|
| Block or escalation | The agent's `code` plus facts (SHA, ref, task ID) | `needs.<code>` |
| PR body | The engine: diff stat, tasks closed, Check results, finding codes | `pr_body` |
| Reviewer finding on a PR | `{severity, lens, rule, file, line}` | `finding.<lens>` |
| Architect or researcher result | `{recommend, options[{id, name}]}`; detail in `<session>/out/` | `options` |
| `status` and run report | `state.json` and the ledger | `status`, `run_report` |
| Envelope `report` and errors | The verb's `data`; `{code}` | `report.<verb>`, `error.<code>` |
| Commit message | `<type>: <task title>` from the backlog | `commit` |
| Waking event in `status --wait` | The event | `event.<type>` |

### 19.3 Template pipeline

| Step | Rule |
|---|---|
| Engine | Go `text/template`, `missingkey=error`, a 6-function map: `code`, `n`, `plural`, `dur`, `path`, `table`; nothing else |
| Typed facts | One Go struct per template in `agent/render.go`; the gate fails a template field with no struct field and a struct field no template uses |
| Order | Facts → fact checks (SHA in repo, path in diff or worktree, task ID exists; a failed fact gets the schema re-prompt) → escape (fact strings as literal code spans) → template → wrap at 80 columns → `output.color` |
| Tables | Over 5 rows: top 5 plus one line `N more: <what>`; a cell over 45 characters moves to a labeled line below |
| Golden fixtures | `embed/output/<profile>/<name>.golden` per template; the gate renders each under both profiles and compares bytes |
| Unlisted cases | Code `other` renders one fixed line, "Needs a decision; details in `<path>`" |
| Model prose | Prose fields such as the builder `summary` feed briefs, the ledger and `<session>/out/`; no template renders them |

### 19.4 Code list

Starts from beta 5's real blocks and escalations (`branch_missing` among them). Beta 6 adds `cancelled`, `paused`, `integration`, `epic_gate`, `dependency`, `flaky`, `tester`, the tester's `pass`, `fail`, `env`, `wrong_command`, `real_failure`, and `config_tampered`, `locked`, `regression`, `lint`, `disputed`, `crash`. A new code is a plan change: schema enum, template and fixture together. Every event `reason` is a code from this list. The gate checks that every code in every agent schema has a template in both profiles.

### 19.5 No leaks

| Path | Guard |
|---|---|
| Orchestrator rules into a worker | A brief holds only role text, repo context, skills and the task; the addendum strips the Komodo block; broken block markers drop the whole addendum and `doctor` reports it |
| Orchestrator skills into a worker | The gate fails a worker `agent.json` listing `run`, `plan`, `respond` or `komodo` |
| Host auto-load | `claude-cli` workers start with `--setting-sources ""`; API links send only the brief |
| A run changing its own guidance | The addendum is read from the base commit; config, settings files and `policy.json` are G3 |

---

## 20. Brief builder

### 20.1 Assembly order (most stable first)

| # | Part | Changes when | Source | Cache |
|---|---|---|---|---|
| 1 | Tool definitions | The agent changes | `agent.json` `tools` | 1-hour breakpoint |
| 2 | Role | The agent changes | `prompt.md` `# Role` | with part 1 |
| 3 | Repo context | The repo's base commit changes | `AGENTS.md` minus the Komodo block, at the base commit | 1-hour breakpoint |
| 4 | Standards | The task's file types change | Skills, `always` first, then by name | 5-minute breakpoint |
| 5 | Task | Every task | The slots | 5-minute breakpoint |

No timestamps, IDs or unsorted maps go before the task. No harness rules appear in any part.

### 20.2 Slots (20)

`R` required (empty fails the brief); `O` optional (empty renders `none`).

| Slot | Source | Builder | Reviewer | Tester | Planner | Architect, researcher | Scout |
|---|---|---|---|---|---|---|---|
| `repo_context` | `AGENTS.md` at base, block stripped | O | O | O | O | O | — |
| `repo_profile` | Detection: languages, command classes | R | R | R | R | O | — |
| `standards` | Skills | O | O | O | — | O | — |
| `group` | Backlog at base: id, title, goal | R | R | R | — | — | — |
| `tasks` | Backlog at base: id, title, card, `done_when` per task | R | R | R | — | — | — |
| `files` | The group's `files` list | R | R | — | — | — | — |
| `file_contents` | Each file in `files` at base; `new file` if absent | R | — | — | — | — | — |
| `diff` | Base → builder head | O (repair) | R | O | — | — | — |
| `checks` | Check results: command, exit code, failing blocks | O (repair) | R | O | — | — | — |
| `findings` | Open reviewer findings | O (repair) | O (re-review) | — | — | — | — |
| `scope_answer` | The `scope` answer | O (resume) | — | — | — | — | — |
| `lens` | Lens text | — | R | — | — | — | — |
| `callers` | Go callers of changed symbols (`go/packages`) | — | O | — | — | — | — |
| `specs` | Spec anchors and decisions the tasks cite | O | O | — | O | O | — |
| `goal` | The request | — | — | — | R | R | R |
| `layout` | Repo tree, paths only | — | — | — | R | R | R |
| `backlog` | Existing epics and groups | — | — | — | O | — | — |
| `repo_map` | Ranked symbol graph fitted to 10% of the ceiling | — | O | — | R | R | — |
| `baseline` | Tests failing at the base commit | — | — | O | — | — | — |
| `lens_rules` | The applicable lenses' rule catalogs | — | R | — | — | — | — |

### 20.3 Ceiling per link

A brief has no per-slot cap. Its only ceiling is half the link's measured context, read before each session.

| Link | Context | Brief ceiling |
|---|---|---|
| Claude Opus 5.5, Sonnet 5.5, Fable 5.1 | 1M | 500K |
| Claude Haiku 4.5 | 200K | 100K |
| OpenAI GPT-5.5 API (later minor) | 1M | 500K |
| OpenAI GPT-5.5 Codex (later minor) | 400K | 200K |
| Local floor: Qwen3.6-27B, Kimi K2.6 class | 262,144 | 131K |

Over the ceiling, the brief goes to the next link in the chain that fits; none left, Blocked `context`. Nothing is cut to fit.

### 20.4 Context rules

- Trimming: Go signature trimming, delta-only diffs on re-review, ANSI and build noise stripped; assembled from cache slices, never by re-reading files.
- Reviewer context comes from the engine only: the task list at the base commit, `done_when`, spec anchors, the diff, Check results, prior findings. Never builder prose.
- Tester context comes from the engine only: the task list and acceptance items at the base commit, the diff, Check results, allowed commands. A restart adds Go's rerun of the disputed item.
- Callers: Go via `go/packages`; other languages find callers with `search`.

---

## 21. Guardrails

### 21.1 Policy row schema (`embed/policy.json`)

| Field | Values | Meaning |
|---|---|---|
| `id` | `G1`…`G6`, `R1`, `B1`, `O1`…`O10` | The row named in a refusal |
| `scope` | Agent names, or `writers`, `runners`, `all`, `orchestrator` | Who it binds to |
| `kind` | `tool`, `path`, `run`, `text` | What the check reads |
| `pattern` | Glob or list | What matches; `~/.komodo/**` style globs expand at load |
| `action` | `refuse`, `warn` | `warn` only for `text` rows on the orchestrator |
| `message`, `alternative` | Template IDs | The refusal line and the allowed alternative |

`safety/rows.go` only loads it; no row is Go code. The gate renders the roster tables from it and fails on drift. The scout's compiled table has 4 rows, the builder's 8.

### 21.2 Rows every agent inherits

| ID | Refuse | Scope |
|---|---|---|
| G1 | A tool not in the agent's set; the server does not list it either | all |
| G2 | Reads or writes outside the session worktree, including `..` paths and symlink escapes | all |
| G3 | Writes to protected paths: `.git` (file or folder), `.git/config`, `.git/hooks`, `bin/komodo`, `~/.komodo/**`, `~/.claude/settings*.json`, `.claude/settings*.json`, `embed/policy.json`, `docs/prd.md`. Memory (`~/.claude/projects/*/memory/`) and plans (`~/.claude/plans/`) are never blocked | writers |
| G4 | Reads of secret files (`.env*` except `.env.example`, `.env.sample`, `.env.template`; `*.pem`; `id_rsa*`, `id_dsa*`, `id_ecdsa*`, `id_ed25519*`; `*.key`), or written content matching a secret pattern | reads all; writes writers |
| G5 | Attribution trailers or session links in any text the agent writes | writers |
| G6 | The `guard.refusal_limit` (3rd) refusal under the same rule; ends the session | all |

### 21.3 Run and builder rows

| ID | Refuse | Scope |
|---|---|---|
| R1 | Any command class not declared; `run` takes `{class, args[]}` | runners |
| B1 | Writes outside the group's `files` and test siblings; the refusal says to stop with result status `needs_scope` | builder |

Facts of `run` that keep their own tests, not guard rows: no shell (pipes, chains and `sh -c` cannot happen); no network (sandbox); no git writes (no class runs git). B2 (a builder's own backlog files) is a lint fact: `doctor backlog` refuses a group whose `files` list `docs/backlog/**`.

### 21.4 Orchestrator rows (text in `AGENTS.md`, any host)

| ID | Refuse |
|---|---|
| O1 | Commit, push, merge, delete or force on `main`, `master`, `release/*`, or any critical ref |
| O2 | Force push, `--force-with-lease`, `+refspec` |
| O3 | Merging into the epic branch by hand |
| O4 | Editing, resetting or removing a worktree the harness has leased |
| O5 | `git stash`; use a WIP commit |
| O6 | `gh pr create` and `gh pr merge`; the engine opens the epic PR |
| O7 | Faking a terminal (`script`, `expect`, `unbuffer`) to run a human-only verb |
| O9 | A human-only verb or any `internal` verb |
| O10 | `--no-verify` on any git command |
| plus | G3, G4, G5 |

The harness enforces these on its own side: verbs refuse leased worktrees and critical refs, and human-only verbs need a terminal.

### 21.5 Mechanics

- Every tool call is checked in-process before it runs (`tool.pre`); input it cannot parse, an unknown tool or a guard error is a refusal. A refusal is logged afterward as `tool.denied`.
- Exactly 3 inline hooks exist, all Go: `tool.pre` (guard), `tool.post` (format after `edit`), `session.end` (WIP commit). No model, repo or host hook runs inside a worker.
- `needs_scope` (B1) is the only refusal a person or the orchestrator can turn into an allow.
- Performance: each agent's rows compile to a matcher once per session; a check is a map lookup plus at most 8 glob tests, under 50 µs. Path containment results are cached per session.

---

## 22. Sandbox and process control

### 22.1 Three layers

1. Tool surface: no native Bash or Edit.
2. Process limits: worktree working directory; environment of only `PATH`, `HOME` (session temp), `TMPDIR`, `LANG`, `RUSTUP_HOME` and the cache variables; timeout; output cap; process and memory caps; whole-tree kill.
3. OS sandbox: wraps every `run` command from the tool server, never the model client.

### 22.2 Per OS

| OS | Sandbox | Profile |
|---|---|---|
| macOS | `sandbox-exec` (seatbelt) | Reads allowed outside home; inside home only the worktree, the repo's git dir, Komodo's caches and toolchain folders; writes only to the worktree, session temp and language caches; network denied except loopback; no process or memory cap, and `doctor` says so |
| Linux, WSL | `bwrap` | `/` read-only; home an empty tmpfs with the worktree, git dir, caches and toolchains bound back; worktree, session temp and language caches writable; `/tmp` tmpfs; `--unshare-net`; `--die-with-parent`; cgroups caps when delegated; when user namespaces are blocked every work verb refuses and names the fix |
| Windows native | Docker when WSL is absent | `--network none`; only the worktree, session temp and language-cache volumes writable; `--pids-limit` and `--memory`; with neither WSL nor Docker every work verb refuses and names both |

| Value | Setting |
|---|---|
| `run` timeout | 10 min per command |
| Processes / memory | 512 / 8 GB per session |
| Policy | `required` on every OS |
| Loopback | Always allowed |
| Profile reuse | Rendered once per worktree; Docker keeps one container per worktree alive |

### 22.3 Caches and toolchains

- Language caches are Komodo's own: `~/.komodo/cache/lang/<repo-id>/`; `GOCACHE`, `GOMODCACHE`, `npm_config_cache`, `CARGO_HOME`, `PIP_CACHE_DIR` point there and are writable. Never the user's `~/.npm`, `~/.cargo/registry`, Go or pip caches.
- Toolchain folders are read-only: `~/.rustup` and the folders holding each detected command's binary; `doctor` runs each command's `--version` in the sandbox.
- Cap `sandbox.lang_cache_gb` 10 (hard 50); the sweep deletes a repo's whole cache when over the cap or unused 30 days, never while the repo holds a lease. `cache clear --kind lang` and `uninstall` remove it.
- Never the per-user process limit: it counts every process the user runs.
- Intake runs `commands.setup` with network on; every `run` keeps it off.

### 22.4 Read-only agents' `run`

Reviewer and tester have no write tools, but their `run` may write the worktree so tests work. After the last read-only session in a worktree ends, the engine resets changed tracked files and fails that review, removes new untracked files that are not gitignored, and keeps ignored build output.

### 22.5 Test sandboxes and copies

| Rule | Detail |
|---|---|
| Blind by construction | The tester's profile lists only its own worktree, the git dir, caches and toolchains; the builder's worktree under `~/.komodo/wt/` is outside that list |
| Two suites at once | Linux `bwrap --unshare-net` and Windows Docker `--network none` give each session a private loopback `[PENDING G-08]`; macOS has no network namespace, so `test`-class runs of the two Verify sessions serialize through one per-repo lock file |
| Service-backed tests | They fail at base too, so the baseline marks them pre-existing and they warn. An item may declare `needs: network \| docker \| service:<name>`; the tester skips it with reason `env` and a ⚠️ line |
| Per-session `TMPDIR` and `HOME` | Two sessions never share a temp dir |
| Copy per language | Go: nothing. Node: copy `node_modules`, symlinks kept. Rust: one `CARGO_TARGET_DIR` per group under the language cache `[PENDING G-12]`. Python: rerun `uv sync`. Makefile-only: rerun `make setup`. A changed lockfile reruns setup instead of copying |
| Copy budget | Counts against Verify's wall time; over 60 s warns; `doctor` reports the mechanism |

| Platform | Mechanism | Status |
|---|---|---|
| macOS APFS | `cp -cR` (clonefile); 1.2 GB with pnpm symlinks cloned in 0.00 s | proven 2026-10-08 |
| Linux | `cp -R --reflink=auto`; reflink on btrfs, XFS, ZFS; byte copy on ext4 | `[PENDING G-09]` per filesystem |
| Windows NTFS | Go byte copy, junctions and symlinks recreated; `core.symlinks false` | `[PENDING G-10]` |
| Windows Docker | The host copies; the tester's container binds the copy at `/work` | `[PENDING G-11]` copy time for 1 GB |

Symlinks are copied as links, never followed; a link resolving outside the worktree is dropped and logged. A `.venv` is never copied. Check adds a case-collision lint on every OS.

### 22.6 Docker backend (native Windows)

| Rule | Detail |
|---|---|
| Image | The full official image per detected language: `golang:<go directive>`, `node:<.nvmrc, else lowest engines major, else current LTS>`, `rust:<rust-toolchain channel, else stable>`, `python:<.python-version, else lowest requires-python, else latest>`. Makefile-only or 2-language repos need `sandbox.image`. Intake pulls it and the ledger records its digest |
| Tools | Rust setup adds `clippy` and `rustfmt` before `cargo fetch`; Python setup is `uv sync` with a `uv.lock`, else `pip install -e .[dev]` when a `dev` extra exists, else `doctor` names the missing tools |
| Runtime | One container per worktree made at Intake with the caps, the worktree at `/work`, a named cache volume per repo; setup with network on, then disconnected; each `run` is a `docker exec`; after setup the container is saved as an image in the background and the tester's container starts from it |
| Kill | A timeout or kill ends the command's process group inside the container; the container restarts only if that fails |
| Host side | `git-read` runs on the host; worktrees cut with `core.autocrlf=false`; `doctor` checks the daemon runs Linux containers |

---

## 23. Scheduler and job templates

| Item | Rule |
|---|---|
| Template | `{name, agents[], slots{}, every}`; names an agent subset, brief slots and an interval, never limits; decoded strictly; lives in `embed/jobs/` or `~/.komodo/jobs/` |
| `jobs tick` | Idempotent, one engine step: enqueue due jobs, fire due `wait_until` timers, drive the pool once; a tick with nothing due exits under 50 ms with no network |
| Callers | A launchd, cron or Task Scheduler entry from `install --schedule`, every 5 minutes; Claude Code `/loop` also works at the cost of a model turn per tick |
| Run id | `job-<name>-<n>`, with its own ledger rows and report; `status --wait` takes it |
| Counters | `jobs/state.json`: `attempt`, `next_at`, `last_fail_hash`, `running` |
| Overlap | A running job holds lease `job-<name>` with a fence; the next tick's queue item is refused as a duplicate and logged |
| Queue | Same queue as groups, `priority: normal`; never preempts a group; fair share treats each job as its own repo |
| Events | `job.due`, `job.fail` |
| Minimum interval | 5 min |
| Retries | 3, backoff 30 s, 2 m, 8 m ±20%; then Blocked, which wakes `status --wait` |
| Shipped | None; one example at `templates/jobs/nightly-review.json` |

Example template:

```json
{"name": "nightly-review", "agents": ["scout", "reviewer"], "slots": {"target": "main..HEAD"}, "every": "24h"}
```

---

## 24. Check

| Step | Rule |
|---|---|
| `done_when` | Every task's `done_when` rerun in the sandbox |
| Scope | Every changed file is in the group's `files` or is a test sibling |
| Secrets | The diff scanned for secret patterns |
| Coverage | Changed-line coverage against `check.patch_coverage` 80; blocks for Go, warns for other languages |
| Baseline | Intake records the `test` class result at the base commit, cached by tree hash (one entry per epic tip, shared by every group cut from it). Passed at base and fails at head: regression, blocks naming the test. Fails at both: pre-existing, warns |
| Impact set | Go: tests of every package importing a changed package (`go list -deps` reversed). Other languages: the full `test` class. The full suite always runs at the epic gate |
| Flake rule | Any failing test is rerun twice alone; two more failures confirm; a split reports `flaky` without blocking and is counted per repo |
| Case collision | Two files differing only by case fail Check on every OS |
| Cache | Keyed by tree hash plus command; an unchanged tree skips the rerun |
| Parallelism | The impact set and `done_when` run in parallel inside the sandbox's process cap |

### 24.1 Scope requests

1. A builder that needs a file outside `files` stops; its final result has status `needs_scope`, the paths and a reason.
2. The engine makes a WIP commit; the resume handle is already in `state.json`; the group goes Blocked `needs_scope`; the lease and worktree stay held and the sweep skips them.
3. The engine denies without asking anyone when a path is protected (G3, G4) or is in another live group's `files`.
4. Otherwise `status --wait` wakes the orchestrator, which answers `scope approve|deny --paths --reason`. Approve adds the paths to the group's `files` (action `scope_change`), logged in the ledger.
5. The engine resumes the same session with the answer in the `scope_answer` slot. One request per task.

---

## 25. Review and the tester

### 25.1 Lenses

| Lens | Applies when | Checks |
|---|---|---|
| `correctness` | always | Logic, error paths, nil and bounds, resource release |
| `security` | always for installer, binary lifecycle, HTTP, tools, guard, sandbox, ship; else on auth, crypto, exec, net, path or env code | Injection, secrets, path escape, unsafe exec, TLS |
| `spec` | The task cites a `docs/lld.md` anchor | The diff matches the cited section, which is in the brief; a mismatch is a `not_met`, not a finding |
| `contract` | An exported symbol changed (Go via `go/ast`; others by export-line diff) | Every caller still compiles and the change is intended by a task |
| `concurrency` | The diff adds `go`, `chan`, `sync`, `atomic`, threads or async | Data races, lock order, cancellation, goroutine leaks |
| `platform` | `_windows.go`, `_darwin.go`, `_linux.go`, path or shell handling | Separators, case, line endings, signals |
| `config` | `embed/*.json`, `defaults.json`, `policy.json` | Hard limits kept, schema version bumped, gate fixture updated |
| `readiness` | always | Tests for changed behaviour; docs and changelog touched when the task says so |
| `quality` | always | Naming, duplication, comment rules from `standards-comments` |
| `economy` | economy mode only | The merged checklist of the rows above at reduced depth |

Rule schema in `embed/lenses/<lens>.json`: `{id, severity, applies: {globs, markers}, check, evidence, source}`. `[PENDING G-16]` the catalogs' content, seeded from Google's code review guide and SWE-bench Verified failure classes, authored in phase 7 with the security lens reviewed by the owner.

### 25.2 Mechanics

| Rule | Detail |
|---|---|
| Profiles | Thorough: every applicable lens in parallel plus a cold pass. Economy: one merged lens. A diff touching only `*.md` runs 3 lenses |
| Severity | `blocker` and `major` block; `minor` reported and counted |
| Verdicts | `met`, `not_met`, `brief_incomplete` per task ID, each exactly once; Go rejects a result that skips or repeats one |
| Added checks | The reviewer runs build, tests and lint itself with `run`; the engine reruns any check a verdict depends on |
| Findings | `submit_finding` takes a rule ID, `file:line` and quoted evidence; blocks only if Go finds the quote at that line in the head or diff; failing findings dropped and logged |
| Precision ledger | Per rule and model: submitted, verified, fixed, `disputed` (a builder code naming file and line); `doctor` lists rules under 50% verified; under 30% for 60 days is a plan change |
| `brief_incomplete` | Blocks with `brief`; a harness fault, not counted against the builder |
| Re-review | Delta only; cached findings by `(lens, rule set hash, diff, model)` cost 0 tokens |
| Epic mode | One verdict per group over the whole epic diff |
| Pool | Lenses run in parallel under one slot each only when `pool_max` allows; else they queue behind the group's slot |

### 25.3 The tester

- Runs beside the reviewer in Verify, in its own throwaway worktree at the same commit, blind to the builder.
- Proves the group's acceptance items from `TG.md` with `run`: pass or fail per item with evidence, and `confirmed | not_confirmed` per task ID exactly once, from `done_when` and the EARS item.
- Its brief says which three gates Go already proved (lint and coverage, success, regression), so it spends its turns on behaviour.
- A failed item counts only after Go reruns its command twice and both fail. If Go's reruns pass, the tester restarts once with the item, its evidence, and Go's command and output, and returns a reason code: `env`, `wrong_command`, `flaky` or `real_failure`. A second dispute blocks with `tester`, a harness fault. A split in Go's reruns blocks with `flaky`.
- Go cross-checks the tester's per-task result with the reviewer's. The tester reruns every item on re-review. The ledger records each tester model's false-fail rate.

---

## 26. Error recovery

### 26.1 Failure table

| Failure | Detector | Action | Bound | Then |
|---|---|---|---|---|
| Transient HTTP | HTTP stack | Retry with backoff | 3 | Session fails |
| Stream dropped mid-answer | HTTP stack | Resend from the start | 1 | Session fails |
| Provider unavailable or unauthorized | Provider | Next allowed link at the session boundary | Chain length | Blocked `no_provider` |
| Provider quota or spend limit | Provider, HTTP | Next session on the backstop; pause with `wait_until` only when no link is left | Reported reset, at most `limits.pause_max_min` | Paused; at the cap Blocked `pause_timeout`, WIP kept |
| Paid link not allowed, or wrong auth on a subscription link | Billing check | Kill before turn 1; drop the link for the run | 1 | Blocked `paid_refused` |
| Schema-invalid result | Tool runtime | One re-prompt | 1 | Task retry |
| Result fails the agent's gate | Tool runtime | The failure goes back into the same session | 2 | `check_fail`, task retry |
| Reply cut off at the output limit | Tool runtime | "continue" | 1 | `no_result`, task retry |
| Timeout or idle | Supervisor | Stop, then a fresh session | Agent `retries` (2) per task | Blocked |
| 3 refusals under one rule | Guard | End the session | 3 | Task retry |
| `done_when` fails | Check | Task retry | Agent `retries` (2) per task | Blocked |
| Regression against the baseline | Check | Back to Build with the failing test's output | `limits.repairs` (3) per group | Blocked `regression` |
| Findings or a failed acceptance item | Check, review | Back to Build in the task's own session | `limits.repairs` (3) | Blocked |
| Epic gate fails | Review | The group that last changed each file returns to Build; a finding with no file blocks the epic | `limits.repairs` (3) | Blocked `epic_gate` |
| Tester fails an item Go's reruns pass | Review | Restart the tester once with Go's output | 1 | Blocked `tester` |
| Go's reruns split | Review | Stop | 0 | Blocked `flaky` |
| A dependency is abandoned | Engine | Stop its dependents | 0 | Blocked `dependency` |
| Check fails on the epic tip after a landing | Engine | Pause landings; the last-landed group returns to Build | `limits.repairs` (3) | Blocked `integration` |
| Builder needs a file outside scope | B1 refusal | WIP commit, end the session, keep the lease | 1 request per task | Blocked `needs_scope`; resumes on approve or deny |
| Flap: same failure hash or zero-diff repair | Recovery | Stop | 1 | Blocked |
| Any model attempt on a task | Recovery | Count it | `limits.task_attempts` (6) | Blocked `attempts` |
| Brief over half the measured context | Brief builder | Next link that fits | 1 | Blocked `context` |
| Landing conflicts with the epic branch | Ship | Merge the epic branch into the group branch, rerun Check | 1 | Blocked `merge_conflict` |
| Budget spent | Budgets | Stop | — | Blocked `budget` |
| Harness crash or reboot | Recovery | Replay the journal after the snapshot | — | Continues |
| Scheduled job fails | Scheduler | Backoff | 3 | Blocked |

### 26.2 Rules

- Loss bound: any single failure loses at most one model turn and no committed edit.
- WIP commit at every session end of a writing agent; an empty diff writes nothing.
- A crash is contained to its group; the run reports it as `Blocked: crash`.
- A task retry stays on its link; only a provider failure moves it.
- Blocked never auto-retries. A group blocked 3 times for one reason is flagged `needs redesign`.
- Stop a worker with SIGINT first (ends the turn, keeps its result), then SIGTERM after 30 s.

---

## 27. Token and resource management

| Budget | Value |
|---|---|
| Group wall | tasks × session wall + 2 × session wall; paused time excluded; hard limit 720 min |
| Per agent | Wall, idle, retries from `agent.json` |
| Per task | `limits.task_attempts` model attempts |
| Per group | `limits.group_tokens`: input, output, cache writes; cache reads reported only |
| Sessions | Pool 1 to 6 machine-wide plus per-link slots |
| Mode | economy (1 merged lens) or thorough (N lenses plus a cold pass) |

Savings: clipping, signature trimming, glob-picked standards, delta diffs, session resume for repair, briefs that fit the link, 4 KB check output, a stable prefix, a sanitizer, per-task sessions, light-weight work on local models, context compaction at 80%.

Prompt caching on the Anthropic API: at most 4 `cache_control` breakpoints per request, 1-hour entries before 5-minute ones, a 512-token minimum on the 5.5 line, writes at 1.25× (5 min) or 2× (1 h) and reads at 0.05× base. Breakpoints follow the prefix order: tools plus role (1 h), repo context (1 h), standards (5 min), task (5 min). `claude -p` caches automatically on a byte-identical prefix. Cached reads do not count toward Anthropic's input-tokens-per-minute limit. The ledger's `cache_read` and `ctx_peak` columns are the proof. `[PENDING G-17]` the 50% hit-rate fixture needs real API sessions, measured in phase 7.

Headline metric: tokens and USD per accepted group, plus cache hit rate per agent, in `status --metrics`.

---

## 28. Cache store

| Item | Key |
|---|---|
| Skill index / bodies | Binary version / content hash |
| Repo profile | Hash of marker files |
| File slices, signature trims | Git blob SHA + trim settings |
| Diff | (base, head) |
| Brief | Hash of every slot input |
| Check result | Tree hash + command |
| Review findings | Lens + diff + prompt hash + model |
| Setup result | Lockfile hash + language |
| Test baseline | Epic tip tree hash + `test` command |
| Repo map, outline lint | Tree hash; tree hash + goal hash |

Every key is `sha256(inputs + binary version)` with the per-run config hash as prefix. Entries are files under a 2-level fan-out; a lookup is one `stat`. Verified on read; LRU under `cache.max_mb` 256 per repo; `cache stats --all` shows the machine total. Build responses are never cached.

---

## 29. Runtime observability

### 29.1 Ledger views

| Row | Fields | Moves on |
|---|---|---|
| Group | `group, run, stage, epoch, fence, lock{card_hash, taken_at}, lease{pid, renewed_at}` | `lock.taken`, `stage.enter`, `lease.lost`, `lock.released` |
| Task | `task, group, state, session, attempts, claimed_at` | `task.claimed`, `task.released`, `task.done`, `task.retry` |

- Task list lock: Intake hashes the group's `TG.md` and `tsk-*.md` into `card_hash` and emits `lock.taken`. While locked, `add backlog --to <group>`, `plan --epic` on that group, and a hand edit refuse with `locked` and the run ID. Landing or abandon emits `lock.released`.
- Task claims: `queued → claimed → done | returned | blocked`; `task.claimed` at `session.start`, `task.released` at `session.end`; a claimed task whose session is dead at the sweep is released with `crash`.
- Views are rebuilt incrementally from the last applied `seq`; `status` on 1,000 rows renders under 100 ms.

### 29.2 Scorecard (`status --metrics`)

Per agent and model over `ledger.retention_days`: first-pass rate, repairs per group, finding precision, tester false-fail rate, tokens and USD per accepted group, cache hit rate per kind. `doctor` suggests a weight change when a light model's first-pass rate beats the standard one over 20 groups. Aggregates are cached by the ledger file's size and mtime.

### 29.3 Live views

`status [--watch]`: pool, stages, budgets, blocks, the group graph (dependencies, each group's stage, which groups touch which files). `status --wait` blocks until the next wake event. `status --explain`: the last 10 events with reasons. `status --session <id>`: the stream tail and result. `status` ends with `Verbs: komodo help`.

---

## 30. Event bus

| Rule | Detail |
|---|---|
| Wakes | `group.blocked`, `group.paused`, `run.done`, `run.stopped` wake `status --wait`; `--wait any` also wakes on `stage.enter` |
| Wake file | A touch under `state/wake/<run>`; the waiter watches the file then reads the journal tail |
| Subscribers, exactly 3 | The ledger, the wake file, the `status --watch` renderer; each a read-only `(event) → error` in `seq` order after `Next()`; an error or panic drops that subscriber for the run |
| Hooks | None on the bus; the 3 inline hooks live in the guard, tools and recovery; repo hooks wait for a later minor and will be subscribers, never gates |
| Performance | Synchronous, in-process; a subscriber must return under 5 ms; the ledger batches appends per tick |

---

## 31. Backlog and planning

### 31.1 Grammar

`docs/backlog/epic-N/tg-N.M/{TG.md, tsk-*.md}`. `TG.md` carries `subsystem:` (a plan ID or `none`), `touches:` (globs), `priority:` (`high` or normal), `files`, dependencies and acceptance items. `tsk-*.md` carries the card, `done_when` and `estimate_lines:`.

### 31.2 Lint (`komodo doctor backlog`)

| Check | Rule |
|---|---|
| Grammar | Shape of every file |
| `files`, `done_when`, dependencies | Present and well-formed |
| Wave isolation | Groups in one wave share no `files` |
| Protected | A group whose `files` include a G3 path or any `docs/backlog/**` path fails |
| Sizing | A task names at most 5 files and 300 estimated lines; a group at most 8 tasks; a chain at most 4 groups; a task over 2× its estimate after Build warns |
| Graph | Cycles refused; warns on a chain over 4, a group every other waits on, a group with over 5 dependencies |
| EARS | `WHEN <event> THE SYSTEM SHALL <response>` (or `IF`, `WHILE`), optional `cmd:` and `needs:` lines; an item without `SHALL` fails |
| Case collision | Two paths differing only by case fail |

### 31.3 Planner

| Pass | Weight | Output | Gate |
|---|---|---|---|
| 1 | heavy | Epic outline: groups, goals, `touches`, dependencies | Go lints; a failure goes back into the session, 2 times, then Blocked `lint` |
| 2 | standard, one session per group in parallel | That group's tasks and EARS items | Go lints each the same way |

- Go derives the DAG from `touches`: two groups whose globs overlap must be ordered; the planner's stated dependency is kept when it agrees, else lint fails naming both groups and the shared file. Groups with no overlap and no dependency start in wave 1.
- `plan <goal>` drafts in memory and checks with `lint_backlog`; the engine lints again and writes files only if lint passes.
- `plan --from <spec>` takes any document set plus the repo. `plan --epic N` adds or amends only unlocked groups; landed and locked groups are read-only inputs.
- `repo_map`: Go builds a symbol graph (Go via `go/ast`; other languages via `embed/langs/<lang>.json` patterns), ranks by inbound references ×3 for files the goal names, and fits to 10% of the brief ceiling by dropping the lowest rank. Cached by tree hash; this repo's map (about 83 files) builds under 1 s. `[PENDING G-18]` patterns for TypeScript, Python and Rust against 3 real repos.
- A 6-group epic plans in about 2 session wall times.

---

## 32. Ship and release

| Item | Rule |
|---|---|
| Branches | Group `komodo/<epic>/<group>`; epic `komodo/<epic>`; only the epic PR targets the base branch |
| Only the harness writes git | Commit, branch, push, merge into the epic branch; never force push, `+refspec`, trailers, session links or critical-ref writes; no agent has forge credentials |
| Ship (0 tokens) | Build the PR body from the template; merge the epic branch into the group branch (no rebase); rerun Check; land with action `land` keyed `(group, fence, merge base)`; a stale fence refuses before any push; one landing at a time; then Check on the epic tip |
| `ship.mode` | `group`: a PR into the epic branch, opened ready (GitHub will not merge a draft), merged by the engine at once. `epic`: `--no-ff` merge straight into the epic branch |
| Epic PR | Opened by the engine into the base once the epic gate passes, keyed `(epic, head SHA)`; a person merges it; `komodo sync` follows |
| Release | Version, tag, cross-built binaries, checksums, ed25519 signature, a changelog fresh at `1.0.0-beta.6`; betas publish as normal releases with "beta" in the version |
| Signing custody | `~/.komodo/release.key` mode 0600 on the owner's machine; one backup in a password manager; releases cut only there; the `next` public key set at the first release. Enterprise: `[PENDING G-21]` KMS or keyless signing and rollback refusal, later minor |
| Performance | Push and PR creation run off the group's `Drive` goroutine |

---

## 33. Installer

### 33.1 Steps

| # | Target | Writes | When |
|---|---|---|---|
| 1 | Binary | `~/.komodo/versions/<v>/komodo`, copied to `~/.komodo/bin/komodo` | always |
| 2 | PATH | Windows: user PATH. macOS and Linux: symlink into `~/.local/bin` if on PATH, else one line in `~/.zshrc` or `~/.bashrc` | if missing; `--no-path` skips |
| 3 | Provider probe | `~/.komodo/state/providers.json` | always; local checks, under 2 s, in parallel |
| 4 | `AGENTS.md` | The full template, or only the `komodo:begin … komodo:end` block | inside a git repo |
| 5 | `CLAUDE.md` | The `@AGENTS.md` import line | Claude detected or `--hosts claude` |
| 6 | `.claude/settings.json` | The `Bash(komodo *)` allow rule and a `SessionStart` hook running `komodo status`; merged, other keys untouched | Claude detected |
| 7 | Manifest | `<git-common-dir>/komodo/install.json`, `v: 1`, one hash per owned file or entry with its owner step | inside a git repo |
| 8 | OS schedule | A launchd, cron or Task Scheduler entry running `komodo jobs tick` every 5 min | `--schedule` only |

- One state machine: compute the desired manifest, diff against `install.json`, apply only differences, run `doctor install`. `uninstall` is the manifest in reverse. `sync` is `install --repo-only` plus fetch and prune. `--dry-run` prints file, action (`write | keep | prune`), reason.
- Binary source: `--from <file>`; else the latest release if newer than the running binary, verified; else a copy of the running binary. An offline install from an older binary points `bin/` at it; fresh moves it forward on the next online check.
- Detection: Claude by `CLAUDE.md`, `.claude/`, `claude` on PATH, or `~/.claude/`; Codex by `.codex/`, `codex` on PATH, or `~/.codex/`; `--hosts` overrides. Codex reads `AGENTS.md`, so step 4 serves it.
- Repo rules: hand edits survive and are reported (`--force` overwrites); a rerun makes 0 changes and prunes stale owned files; the pin only rises; `--project` adds `docs/{prd,hld,lld}.md`, `CHANGELOG.md` and a PR template. Never created: a repo config folder, git hooks, `.codex/`, `~/.claude`, `~/.codex`, or any `.claude/` skill, rule or guard hook.
- No config file is written; `~/.komodo/config.json` appears on the first `komodo config` write.

### 33.2 Provider probe

| Link | Check |
|---|---|
| `claude-cli` | Absolute path, version, `claude auth status --json` (login method and plan), one `system/init` line per CLI version `[PENDING G-02]` |
| `anthropic-api` | The key reference resolves |
| `codex-cli` | `codex` on PATH and its version; usable in the later minor |
| `openai-api` | The key reference resolves; usable in the later minor |
| `http:<name>` | `GET <url>/models` answers within 1 s and lists its models; `GET /api/version` detects Ollama |

The probe result is the capacity profile's source, reused until `install`, `doctor` or `fresh` refreshes it.

### 33.3 The Komodo block in `AGENTS.md`

```markdown
<!-- komodo:begin min=1.0.0-beta.6 -->
## Komodo harness

- **You are the orchestrator.** Drive Komodo only through `komodo <verb>`; with no terminal, every reply is the JSON envelope.
- **Find your verbs:** run `komodo help --json` first. It lists every verb, flag and result schema, and marks the terminal-only ones.
- **Your verbs:** `plan`, `run`, `status`, `skill`, `scope`, `add`, `stop`, `resume`, `abandon`, `sync`, `doctor`, `gate`, `jobs tick` and config reads. Run `status --wait` in the background.
- **Not yours:** `install`, `fresh`, `config` writes, `gate --install` and `release` belong to the person. They refuse without a terminal. Hand the person the command.
- **Start:** run `komodo status` first: version, live runs, blocked groups.
- **Skills:** `komodo skill get run|plan|respond|komodo` returns the workflow for each job.
- **Rules:** <O1–O7, O9 and O10, from `embed/rules/orchestrator.md`>
- **Report:** relay the envelope's `report` field verbatim; it is already in the output contract.

<output style for output.profile, from embed/output/adhd.md>
<!-- komodo:end -->
```

### 33.4 Bootstrap

macOS, Linux, WSL: `curl -fsSL <repo>/install.sh | sh`. Windows: `irm <repo>/install.ps1 | iex`. Both download the latest binary, check SHA-256 against `latest.json`, and run `komodo install --machine-only`. The scripts verify SHA-256 over HTTPS only; the binary verifies ed25519 on every later fresh.

---

## 34. Binary lifecycle

| Step | What | When |
|---|---|---|
| 1 Check | Fetch the ~1 KB `latest.json` in-process with a 1 s timeout; record `latest` in `state/fresh.json`. Only `status`, the engine at run start and `fresh --check` do this | at most once per `fresh.interval` (1 h) |
| 2 Spawn | Any verb compares `latest` with its own version; only if newer does it spawn `internal fresh-bg` detached | a local file read |
| 2b Download | To `versions/<v>/komodo.part`; verify SHA-256 and ed25519; rename into place | seconds, background |
| 3 Swap | Atomically replace `bin/komodo`; Windows renames the running `.exe` to `.old` first | immediately |
| 4 Use | Every new call gets the new binary | the hot swap |
| 5 Pin | Live runs keep their version; the engine starts every child from `versions/<its v>/komodo`, never `bin/` | until each run ends |
| 6 Cleanup | Delete versions neither current, previous, nor used by a live run in `state/active/` | every fresh |

| Concern | Design |
|---|---|
| Source | `github.com/rdevitto86/komodo-agentic-factory-coding/releases/latest/download/latest.json`; a release download is not a REST call, so the 60-per-hour limit does not apply |
| Manifest | Version, URL and SHA-256 per OS and arch, ed25519 signature; current and next public keys embedded |
| One updater | `state/fresh.lock`, created exclusively, stale after 10 min |
| Direction | Background fresh never downgrades; `fresh --to <v>` and `install` may; a dev build (`+dev.<commit>`) is replaced only by a strictly newer release |
| Failure | Silent, logged to `state/fresh.log`; after 3 failures in a row `status` and `doctor` say so |
| Off switch | `fresh.auto: false`, or `CI=true` |
| `--restart-runs` | Each live run gets a stop signal and checkpoints at the next safe point between tool calls; exits with reason `restart`; `fresh` resumes it on the new binary; a run not checkpointed in 2 min stays on its version and is reported; one in-flight model turn may repeat |
| Version skew | A new binary reads state written by the previous schema version; older than that, `status` says to let the run finish or use `fresh --restart-runs` |
| Repo pin | `<!-- komodo:begin min=<v> -->` in `AGENTS.md`; below it, verbs refuse with `run komodo fresh` |

---

## 35. Gate and self-checks (`komodo gate`, Komodo repo only)

| Check | Rule |
|---|---|
| Build and test | `go vet`, tests, cross-builds for windows, linux, darwin |
| Structure | Import direction, comment lint, strict decode of every `agent.json` and job template |
| Safety | The guard table and the prefix-hash pin |
| Events | Every emitted type is in the catalog; every catalog type has an emitter |
| Docs drift | A verb missing from `README.md` or the `komodo` skill, a broken decision link, an `lld.md` package that does not exist |
| Maps | Every task group names a plan ID, and every plan ID has an `lld.md` entry (Appendix A) |
| No silent gaps | Every `[PENDING G-NN]` tag in `docs/lld.md` names a backlog task that closes it, and every gap row has a phase; a tag with no task fails the gate |
| Generated artifacts | Roster tables from `policy.json`; `events.schema.json` from the enum; `skills/index.json` hashes; standards rule IDs; template golden fixtures under both profiles; the action table against the kinds `Next()` emits |
| Timing | Short verbs under 50 ms; the gate under 3 min locally; a 10,000-event replay under 1 s `[PENDING G-19]` |

Checks run in parallel by package; cross-builds run last. `--fast` skips cross-builds and timing. `--install` places a dev build at `~/.komodo/bin/komodo`; `--install-hooks` installs git hooks in the Komodo repo. Each failing case has a fixture that makes the gate fail.

---

## 36. Edge cases

| Situation | Behaviour |
|---|---|
| Crash between `action.start commit` and `action.done` | Replay reruns the commit; compare-and-swap on the hidden ref makes it one commit |
| Torn last journal line | CRC mismatch drops it; `journal.truncated` emitted once |
| Two engines on one group | Create-only lease: exactly one holder; the loser's landing is refused by git with exit 128 and ends in `lease.lost` |
| A cancelled Verify session reports late | Its epoch is older; journaled `stale: true`; ignored |
| Both Verify sessions fail in one tick | Handled in `seq` order; the second is stale |
| Model ID returns 404 mid-run | That session start fails; the next session takes the prior ID, then the backstop |
| Ollama left at 4,096 context | Below the floor; `doctor` fails it and names `OLLAMA_CONTEXT_LENGTH=262144`; the engine skips it |
| Brief exceeds half the measured context | Next link that fits; none left, Blocked `context`; nothing is trimmed |
| Hand-edited `config.json` | Every verb refuses `config_tampered` until `config --accept` |
| Repo pinned above the binary | Only `help`, `version`, `fresh`, `install`, `doctor` run |
| Offline install from an older binary | `bin/` points at the older version; fresh moves forward on the next online check |
| Two Verify sessions bind one fixed port | Private loopback on Linux and Docker `[PENDING G-08]`; serialized `test` runs on macOS |
| A test needs a database or public host | Fails at base and head; pre-existing; warns, never blocks; `needs:` items skip with `env` |
| A pre-existing failing test | Baseline marks it; the group lands with a warning |
| A test fails once and passes twice | Reported `flaky`; the group lands; counted per repo |
| The tester fails an item Go passes twice | Tester restarts once with Go's output; a second dispute blocks `tester` |
| Two files differ only by case | Check fails on every OS |
| A symlink resolves outside the worktree | Dropped at copy time and logged |
| The builder changed a lockfile | Setup reruns for the tester instead of copying |
| A finding with no file at the epic gate | The epic blocks with `epic_gate` |
| A finding's quote is not at the line | Dropped and logged; does not block |
| A dependency is abandoned | Dependents block with `dependency` |
| `needs_scope` on a protected path or another group's file | Denied by the engine without asking |
| Paid link under `providers.paid: never` | Removed from the chain; Blocked `paid_refused` naming the key |
| `ANTHROPIC_API_KEY` set with a Max login | Env scrubbed; the init line must say `apiKeySource: none` or the session dies before turn 1 |
| Max draws overage credits | One warning; the session finishes |
| Pause exceeds `limits.pause_max_min` | Session cancelled, WIP kept, Blocked `pause_timeout` |
| `resume` before the provider reset | Every paused agent attempts, is refused at 0 turns, re-pauses with the new reset |
| `kill -9` on a paused engine | `jobs tick` or `status` after `wait_until` resumes the run |
| Native Windows with neither WSL nor Docker | Every work verb refuses and names both |
| Linux with user namespaces blocked | Every work verb refuses and names the fix |
| Windows long paths off | `doctor` warns and names the fix; worktree names are short hashes |
| A planted `CLAUDE.md`, user hook or auto-memory file | 0 effect on a worker (`--setting-sources ""`) |
| A steering phrase in a skill | Fails the gate |
| A steering phrase in the repo's `AGENTS.md` | `doctor` warns; the brief still loads |
| Broken Komodo block markers in `AGENTS.md` | The whole addendum is dropped from briefs; `doctor` reports it |
| The builder edits `AGENTS.md` mid-run | Its next brief still carries the base-commit text |
| A worktree unused 3 days | WIP-committed to the hidden ref and removed; `resume` rebuilds it |
| A reviewer's test edits a tracked file | Reset by the engine; that review fails |
| A `run` arg of `&&` | Reaches the command as a literal string |
| Fresh during a live run | The next `komodo version` is new; the run finishes on its old version |
| Bad checksum or signature on fresh | The old binary stays |
| Two concurrent verbs see a newer release | One updater, via `fresh.lock` |
| A group blocked 3 times for one reason | Flagged `needs redesign` in `status` |

---

## 37. Verification plan

| Check | Expected |
|---|---|
| Every phase | `go run ./cmd/komodo gate` passes; `GOOS=windows|linux|darwin go build ./...` cross-builds |
| Full run | `komodo install`, then `komodo run tg-fixture` on a local model and `komodo status --wait done`, ends with the group landed per `ship.mode` and one ledger row per session |
| Crash | `kill -9` mid-run, then `resume`, replays the journal and finishes with 0 repeated paid sessions |
| Race | 2 engines on one group make exactly 1 landing; the loser ends in `lease.lost` and the hidden ref holds the winner's commit |
| Regression | A fixture with a pre-existing failing test lands; a seeded regression blocks with `regression` |
| Fresh repo | Following `README.md` on macOS and Windows reaches a shipped fixture group |
| Scheduled | A `tick` from an `install --schedule` entry runs a fixture job; 3 forced failures end Blocked and wake `wait` |

Each component's proof cases are the fixtures behind its gate checks; the security lens is required for the installer, binary lifecycle, HTTP stack, tool runtime, guardrails, sandbox and ship, even in economy mode.

---

## 38. Cutover from beta 5

Beta 5's global hooks call `~/.komodo/bin/komodo`, the path beta 6 installs to; beta 6 would answer `komodo guard` with exit 2, which Claude Code treats as a block. Before phase 2, by hand: remove the `Bash(python -m komodo:*)` allow rule, the `komodo guard` PreToolUse hook, the `komodo hook status` and `komodo hook prune` hooks, and the 36 `skillOverrides` from `~/.claude/settings.json`; delete the 5 Komodo skills under `~/.claude/skills/` (leave `synced/`); update the 5 stale lines in `~/.claude/AGENTS.md`. Phase 7: `komodo gate --install`. Phase 8: `komodo install` in this repo. Phase 9: the cutover column and the repo table; `doctor install` confirms 0 shadowing binaries and 0 Komodo hooks.

---

## 39. Gap register

Every `[PENDING G-NN]` tag in this document resolves to one row here. From phase 1 the gate fails a row with no backlog task until it closes.

| ID | Area | Gap | Closes by |
|---|---|---|---|
| G-01 | Release signing | Key location and custody | Decided 2026-10-09: local key file plus a backup copy (§32) |
| G-02 | Provider probe | Capturing the `system/init` line at probe time without a model turn | Spike, phase 4 |
| G-03 | Local models | A 24 GB machine running Qwen3.6-27B at 262,144 context | Spike, phase 4 |
| G-04 | Pool | `OLLAMA_NUM_PARALLEL` above 1 on one machine, deciding whether `limits.link_sessions.local` can exceed 1 | Spike, phase 4 |
| G-05 | Model registry | Past exact IDs for a model that retires mid-run | Decided 2026-10-09: `providers.json` is the registry; a 404 fails over (§16.6) |
| G-06 | Models | Whether a Max login exposes `claude-fable-5-1` to `claude -p` | The install probe records it, phase 4 |
| G-07 | Limits | The real Max 5-hour window against `limits.link_sessions.claude-cli` 3 | Ledger measurement over the first 20 groups, phase 7 |
| G-08 | Sandbox | `bwrap --unshare-net` gives each session a private loopback | Spike, phase 2 |
| G-09 | Sandbox | `cp --reflink=auto` behaviour on ext4 and btrfs, symlinks kept | Spike, phase 2 |
| G-10 | Sandbox | Windows symlink and junction handling in the byte copy, `core.symlinks` | Spike, phase 2 |
| G-11 | Sandbox | Docker Desktop bind-mount copy time for a 1 GB `node_modules` | Spike, phase 2 |
| G-12 | Sandbox | Whether a copied Rust `target/` is reused or rebuilt | Spike, phase 2 |
| G-13 | Guard, skills | The steering-phrase list is 6 entries by judgment | Tune from `doctor` warnings, phase 3 |
| G-14 | Supervisor | A mid-turn nudge reaching a turn stuck in a tool call | Best effort decided; the proof accepts a late nudge, phase 5 |
| G-15 | Scope | Whether `scope approve` needs a human | Decided 2026-10-09: the orchestrator answers after 2 engine denials (§24.1) |
| G-16 | Review | The lens rule catalogs' content | Authoring task, phase 7; security lens reviewed by the owner |
| G-17 | Budgets | The 50% cache-hit fixture needs real API sessions | Measured in phase 7 |
| G-18 | Planning | `repo_map` patterns for TypeScript, Python and Rust against 3 real repos | Spike, phase 7 |
| G-19 | Timing | The 10,000-event replay under 1 s and the 50 ms short-verb budget | Gate timing, phase 6 |
| G-20 | OpenAI | The `codex-cli` link, `openai-api` chain and `.codex/` install | Later minor, by design |
| G-21 | Release signing, enterprise | Private key in a KMS or keyless signing; `fresh` refuses a manifest older than the last accepted | Later minor; trigger: the first machine the owner does not own runs `fresh` |
| G-22 | Model registry, enterprise | A central gateway owning IDs, `retired_on` dates, quotas; `doctor` warns 30 days ahead | Later minor; trigger: 3 or more machines, or 2 people sharing one cap |
| G-23 | Scope, enterprise | Path ownership rows in `policy.json`; a named human for other teams' paths; a widening digest in `status --metrics` | Later minor; trigger: the first repo with 2 teams or customer data |

Rule for new gaps: no section may say "later", "TBD" or carry a `[PENDING]` tag without a row here.

---

## Appendix A. Traceability to the plan

| Plan ID | Component | LLD sections |
|---|---|---|
| S01 | CLI processor | §7 |
| S02 | Installer | §33 |
| S03 | Binary lifecycle | §34 |
| S04 | Configuration | §8 |
| S05 | Provider adapters | §16 |
| S06 | HTTP stack | §17 |
| S07 | Agent registry | §14 |
| S08 | Tool runtime | §15 |
| S09 | Skill loader | §18 |
| S10 | Rules and output contract | §19 |
| S11 | Brief builder | §20 |
| S12 | Guardrails | §21 |
| S13 | Sandbox and process control | §22 |
| S14 | Orchestration engine | §10, §11 |
| S15 | Workspace and leases | §12 |
| S16 | Session supervisor | §13 |
| S17 | Scheduler and job templates | §23 |
| S18 | Error recovery | §9.4, §26 |
| S19 | Check | §24 |
| S20 | Review and evidence | §25 |
| S21 | Token and resource management | §27 |
| S22 | Cache store | §28 |
| S23 | Runtime observability | §29 |
| S24 | Event bus | §9, §30 |
| S25 | Backlog and planning | §31 |
| S26 | Ship | §32 |
| S27 | Gate and self-checks | §35 |

## Appendix B. References

| Reference | Use in this document |
|---|---|
| `docs/hld.md` | Rationale, views and quality goals for every mechanism specified here |
| `PLAN.md` at commit `ba4ff823` | Source of every fact until phase 1 removes it |
| `drafts/README.md` | The 20 brief slots and the ceiling per link |
| IEEE software design description practice (ISO/IEC/IEEE 42010 views; SDS module specifications) | Section shape: per-component files, interfaces, data, algorithms, limits, failure handling |
| Claude Code headless docs, https://code.claude.com/docs/en/headless | Launch line, stream facts, resume, signals |
| Claude Code environment variables, https://code.claude.com/docs/en/env-vars | MCP timeouts, output caps, retry watchdog |
| Anthropic prompt caching, https://platform.claude.com/docs/en/build-with-claude/prompt-caching | Breakpoints, minimums, pricing multipliers |
| Anthropic rate limits, https://platform.claude.com/docs/en/api/rate-limits | Retry and limit classes, headroom headers |
| Anthropic models overview, https://platform.claude.com/docs/en/models/overview | Model IDs, windows, prices |
| Ollama FAQ, https://docs.ollama.com/faq | Context length, parallelism, keep-alive defaults |
| Codex CLI security, https://developers.openai.com/codex/security | Sandbox profiles per OS |
| Codex config reference, https://learn.chatgpt.com/docs/config-file/config-reference | Stream idle ceiling, retry default |
| AWS exponential backoff and jitter, https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter | Full-jitter formula |
| Distributed leases with fencing tokens, https://hackernoon.com/building-distributed-leases-with-consensus-heartbeats-and-fencing-tokens | Fence semantics |
| Temporal retry policies, https://docs.temporal.io/encyclopedia/retry-policies | The rejected unlimited-retry default |
| Google Testing Blog on coverage, https://testing.googleblog.com/2020/08/code-coverage-best-practices.html | The 80% changed-line bar |
| Kiro spec-driven development, https://verdent.ai/guides/agents/kiro-spec-driven-development | EARS item grammar |
| Aider repo map, https://github.com/pdavis68/RepoMapper | Repo map ranking |
