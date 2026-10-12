# Beta 6 architecture review guide

One card per subsystem, for sign-off one at a time. Every fact here comes from `PLAN.md` at commit `ba4ff823`; the `Source` line on each card names the lines. This file is a review aid and leaves with `PLAN.md` after phase 1.

**How to use it:** read a card, then answer with `S<nn> approved`, or name the bullet to walk back. Each approval becomes a row in `PLAN.md`'s Review changes table. Nothing is built until every card you care about is approved.

## The harness in one pass

**What it is:** one Go binary, standard library only, that turns a backlog of task groups into landed feature branches using stateless model sessions. The orchestrator (Claude Code, a person at a shell, later Codex or `komodo chat`) only calls verbs; the engine does the work.

**The spine:** facts become typed events in a per-group state; one pure function `Next(state, event)` decides the next state and actions; nothing else moves a group. [PLAN.md:84-123]

**The flow per group:** `Intake -> Build -> Check -> Verify -> Ship` (plus `Paused`, `Blocked`). [PLAN.md:138-156]

**Who calls whom:** `S01 CLI -> S14 engine -> S16 sessions -> S05 providers -> S08 tools` with `S12 guard` and `S13 sandbox` around every tool call. [PLAN.md:60-76]

## Layers and review order

| Layer | Subsystems | Package |
|---|---|---|
| Foundation (§1) | event model, gates | `engine`, `ops` |
| Interface | S01 S02 S03 S04 | `cli`, `host`, `core` |
| Model I/O | S05 S06 S07 S08 | `provider`, `tools`, `agent`, `core` |
| Context | S09 S10 S11 | `agent` |
| Safety | S12 S13 | `safety` |
| Orchestration | S14 S15 S16 S17 S18 | `engine` |
| Quality | S19 S20 | `quality` |
| Economy and ops | S21 S22 S23 S24 | `ops`, `core` |
| Delivery | S25 S26 S27 | `repo` |
| Core pillar | S29 | `state` |

**Recommended order:** §1, S14, S04, S01, S07, S15, S12, S06, S08 (the critical path), then S18, S16, S17, S19, S20, S21, then the rest. A walk-back on the critical path ripples into the other cards, so it goes first.

**"Decided for you" tags:** 20 defaults the design pass chose without you. Each card lists its own under `Decided for you`. Walk back any.

---

## §1 · Event model and gates (foundation, not a card)

**High level:** every stage move, error and retry is an event appended to `runs/<group>/events.jsonl` before it is applied; `Next()` is pure and recorded actions (the outbox) make every side effect safe to run twice. This is Temporal's workflow-and-activity split applied to one process.

**Internals:**
- **Envelope:** `v, seq, ts, run, group, session, epoch, stale, type, reason, data, crc`; 32 event types in 7 families.
- **Epochs:** each stage entry raises `epoch`; a late event from a cancelled session is recorded `stale: true` and ignored. This is how Verify cancels one of two sessions safely.
- **Outbox:** `action.start` before, `action.done` after; `id = sha256(seq, kind, args)`; on replay a done action is skipped, a started one reruns under its idempotency key (S18 table).
- **Gates as events:** Intake, Build, Check, Verify, Ship each exit only when named events have been seen; the engine's checks decide, never a model's "done".
- **Verify race:** first verified failure cancels the other session and re-enters Build under a new epoch.
- **Guarantees:** ordered gap-free, durable before applied, exactly-once state moves, at-least-once actions, no lost wake, contained subscriber faults.

**Fits with:** S14 implements `Next()` and `Drive`; S24 owns the envelope and delivery; S18 owns the action key table; S23 reads the group state as a subscriber.

**Source:** [PLAN.md:84-206]

---

## S01 · CLI processor

**High level:** one verb set for you and the orchestrator, one JSON envelope, 5 exit codes. The CLI never reads state files itself; it parses, validates, resolves, dispatches and renders.

**Fits with:** every subsystem through thin calls; S10 renders every `report`; S03 pin check runs first; S04 hash guard runs in step 2.

**Internals:**
- **5-step pipeline:** parse (one verb table, `--input` JSON under explicit flags), validate (types, closest match, terminal check, pin, config hash), resolve (repo id, config, capacity profile), dispatch, render. Steps 1 to 3 make 0 writes.
- **Envelope:** `{v, ok, verdict, report, data, error{code, reason, fix}}`; `--json` is default without a terminal.
- **Tiers:** shared, terminal-only (refused without a controlling terminal), internal (`internal tools`, `internal fresh-bg`).
- **Idempotent writes:** a repeated writing verb reports `0 changes`; every writer takes `--dry-run`.

**Commands owned (24 verbs):** `help, version, install, uninstall, fresh, doctor, config, sync, add backlog|changelog, plan, run, status, skill, scope, resume, stop, abandon, jobs, cache, gate, release`, plus `internal tools|fresh-bg`.

**Global flags:** `--json, -C, --input, -q/-v, --no-color, -h`.

**Config keys:** none owned; reads `output.*` for rendering.

**Decided for you:** `install` flags trimmed from 13 to 8 (`--hosts` replaces 4).

**Gaps:** G-19 (50 ms short-verb budget is a target).

**Source:** [PLAN.md:258-370]

---

## S02 · Installer

**High level:** `komodo install` puts the binary on the machine and the Komodo block into the repo's `AGENTS.md`, through one manifest-driven state machine. Rerunning it is how you re-probe. Offline works.

**Fits with:** S03 for the binary and pin, S04 for the probe result that feeds the capacity profile, S05 for provider detection, S17 for `--schedule`.

**Internals:**
- **8 steps:** binary, PATH, provider probe, `AGENTS.md` block, `CLAUDE.md` import, `.claude/settings.json` (2 entries only), manifest `install.json`, OS schedule.
- **Manifest diff:** desired manifest vs `install.json`; apply only differences; `uninstall` is the reverse; `sync` is `--repo-only` plus fetch and prune.
- **Hand edits survive:** hash mismatch leaves the file alone and reports; `--force` overwrites.
- **Never created:** a repo config folder, git hooks, `.codex/`, anything else in `.claude/`.
- **Bootstrap scripts:** download, verify SHA-256, exec `komodo install --machine-only`.

**Commands owned:** `install` (`--repo-only, --machine-only, --hosts, --no-path, --from, --project, --schedule, --force`), `uninstall`, `sync`.

**Config keys:** none; writes `state/providers.json`.

**Files owned:** `host/{install,claude,codex,probe}.go`, `templates/`, `install.sh`, `install.ps1`.

**Decided for you:** PATH edit strategy per OS; detection by PATH alone counts.

**Gaps:** G-20 (`.codex/` install is a later minor).

**Source:** [PLAN.md:372-461]

---

## S03 · Binary lifecycle (`fresh`)

**High level:** every new `komodo` call runs the newest release with no prompt and no model. Versions live in `~/.komodo/versions/<v>/`, `bin/komodo` is a copy of the current one, and live runs stay pinned to the version they started on.

**Fits with:** S01 pin check, S02 binary step, S06 for the download, S26 release signs what this verifies.

**Internals:**
- **Hourly check:** 1 KB `latest.json` fetch with a 1 s timeout, only from `status`, run start and `fresh --check`; short verbs read `fresh.json` only.
- **Background swap:** `internal fresh-bg` downloads to `.part`, verifies SHA-256 and ed25519, renames into place, replaces `bin/komodo` atomically (Windows renames the running `.exe` to `.old`).
- **Runs never block a swap:** the engine starts children from `versions/<v>/`, never `bin/`; `--restart-runs` checkpoints each run within 2 min and resumes it on the new binary.
- **Repo pin:** `min=` in the `AGENTS.md` block; below it, verbs refuse with `run komodo fresh`.
- **Signing:** ed25519, current and next public keys embedded; private key at `~/.komodo/release.key` with a backup copy (your decision, G-01).

**Commands owned:** `fresh` (`--check, --to, --restart-runs`), `version`, `internal fresh-bg`.

**Config keys:** `fresh.auto` (true), `fresh.interval` (1h, min 10m).

**Decided for you:** the 4 Vet lines: versioned hot swap, hourly check points, 2-minute restart window, betas as normal releases.

**Gaps:** G-21 (enterprise signing, later minor).

**Source:** [PLAN.md:463-530]

---

## S04 · Configuration

**High level:** one machine file, `~/.komodo/config.json`, one write path, `komodo config`. Two layers only: built-in defaults with hard limits, then the machine file. No repo layer, no per-run override, no env besides `KOMODO_HOME` and `NO_COLOR`.

**Fits with:** every subsystem reads it once per process; S01 enforces the hash guard; S05 and S11 consume the capacity profile; S13 runs the detected commands.

**Internals:**
- **33 flat keys** plus `agents.<name>` (4 keys) and `endpoints.<name>` sections; strict decode with closest-match errors; values over a hard limit are refused.
- **Hash guard:** `state/config.sha`; a hand edit makes every verb refuse with `config_tampered` until `config --accept`.
- **All or none:** a multi-key write applies every pair or none; one audit row per changed key in `state/config.log`.
- **Snapshot per run:** the effective config hash goes into `state.json`; mid-run edits apply to the next run.
- **Capacity profile per link:** `{ctx, slots, cost_class, effort}` derived before each session; swapping Claude for a local link changes the profile, never the engine.
- **Command detection:** `go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `Makefile` map to setup/build/test/lint/format; only defined scripts and targets are used; no shell.

**Commands owned:** `config [agent|endpoint <name>] <key>[=<value>]…` (`--all, --default, --accept`).

**Config keys owned (the notable ones):** `mode`, `output.*`, `providers.paid`, `providers.ctx_floor`, `limits.pool_max` 6, `limits.link_sessions.<link>` 3, `limits.group_wall_min` (tasks × session wall + 2 × session wall, hard 720), `limits.task_attempts` 6, `limits.returns` 3, `guard.refusal_limit` 3, `check.patch_coverage` 80, `sandbox.*`, `cache.max_mb` 256, `ship.mode` group.

**Decided for you:** group wall hard limit 720; `check.patch_coverage` 80 blocks Go only; `adhd` output profile on every machine; `env:` key references only.

**Gaps:** G-04 (Ollama parallel sessions), G-07 (real Max window vs 3 per link).

**Source:** [PLAN.md:532-687]

---

## S05 · Provider adapters

**High level:** one interface (`Probe`, `Start`, `Resume`) over 5 links; Claude CLI first, free open-source HTTP endpoints as the mandatory backstop, the paid API only as `providers.paid` allows. Never pay by accident.

**Fits with:** S06 carries HTTP links; S08 runs the tool loop for API links and the private MCP server for CLI links; S16 reads the stream; S18 handles pauses and failover.

**Internals:**
- **Chain per agent:** fixed in `agent.json`; `claude-cli` (latest, then prior model per weight) → each free `http:` endpoint → `anthropic-api`. A skip is a `provider.skip` event with reason.
- **Context is measured:** Models API `max_input_tokens`, Ollama `/api/ps`, or the endpoint's typed `ctx`; floor 262,144 for `http:` endpoints; briefs are never shrunk to fit.
- **Billing check, 3 mechanical steps:** `claude auth status --json` sets the cost class; env scrub removes API keys from the worker; the first stream line must say `apiKeySource: none` or the session dies with exit `billing`.
- **Limits pause, never stop:** on a quota halt the next session goes to the backstop; with no link left the group pauses with `wait_until` in the ledger and `quota.json` machine-wide; `limits.pause_max_min` 360 bounds it.
- **Launch line (`claude -p`):** stream-json both ways, `--tools ""`, private MCP only, `--permission-mode dontAsk`, `--permission-prompts none`, `--setting-sources ""`, `--json-schema`.
- **Schema enforcement per link:** `--json-schema`, structured outputs, `response_format`; thinking stays on; effort fixed per weight.

**Commands owned:** none directly; feeds `doctor` (catalog table) and `status` (model per task).

**Config keys:** `providers.paid` (never), `providers.keys.*`, `providers.ctx_floor` 262144, `endpoints.<name>.{url, models, ctx, key, self_hosted, first_byte_s}`, `warn.usd_per_run` 5, `warn.quota_pct` 90.

**Files owned:** `provider/{provider,billing,claude,codex,anthropic,openai}.go`, `embed/models.json`, `state/providers.json`, `state/quota.json`.

**Decided for you:** Haiku 5.5 as light; `--permission-prompts none` in the launch line; `providers.json` is the only model registry (G-05, yours).

**Gaps:** G-02 (probe the init line without a model turn), G-03 (27B at 262K on 24 GB), G-06 (`fable` under Max), G-20, G-22.

**Source:** [PLAN.md:691-861]

---

## S06 · HTTP stack

**High level:** one client for the API links, the `web` tool and the updater, with a host allowlist per caller, full-jitter retries, and redaction of every key.

**Fits with:** S05's `anthropic.go` and `openai.go`, S03's updater, S08's `web`, S16's idle timer via the SSE reader.

**Internals:**
- **Retry classes:** reset, 408, 429 with `retry-after` ≤ 60 s, 5xx, 529; 4 attempts, sleep random(0, min(30 s, 1 s × 2^attempt)).
- **Returned as a limit, not retried:** spend-cap 429 (`enforced_spend_limit_reached`), `retry-after` over 60 s, quota errors in the body.
- **Timeouts:** connect 10 s, first byte 60 s (loopback 300 s), stream idle 90 s hosted or 300 s loopback.
- **Headroom:** `anthropic-ratelimit-*` headers raise `limit.warn` before any 429.
- **TLS:** system roots plus `http.ca_bundle`; plain `http://` only to loopback or `self_hosted`; same-host redirects only; 8 MB body cap.

**Config keys:** `http.ca_bundle`, per endpoint `first_byte_s`.

**Decided for you:** none beyond the 2026-10-06 approvals.

**Source:** [PLAN.md:863-896]

---

## S07 · Agent registry

**High level:** 7 agents, each an `agent.json` (what Go reads) and a `prompt.md` (what the model reads). Weights heavy, standard, light pick the model per link; chains are fixed in the file, never in config.

**Fits with:** S05 picks the model from weight and chain; S08 binds `tools`; S09 reads `skills`; S11 reads `slots`; S12 reads `guard`; S14 reads `pool_slot`.

**Internals:**
- **Fields:** `v, weight, chain, tools, run_classes, guard, limits{wall_min, idle_min, retries}, skills{allow, always}, slots{required, optional}, result (JSON Schema), pool_slot`.
- **Registry at start:** decode once per process; cross-checks that result `$id`s are unique, every `guard` row exists in `policy.json`, every tool exists in S08.
- **Roster:** planner, architect, reviewer heavy; builder, researcher standard; tester, scout light.
- **Limits are time and retries only:** wall 60 min (hard 120), idle 10 (hard 30), retries 2 (hard 3); no turn or token caps per agent.

**Commands owned:** none; `config agent <name>` writes its 4 override keys.

**Config keys:** `agents.<name>.{model, session_wall_min, session_idle_min, retries}`.

**Files owned:** `agent/agent.go`, `embed/agents/*/{agent.json, prompt.md}`.

**Decided for you:** `fable` as an opt-in heavy model only; no repo-defined agents.

**Gaps:** G-06.

**Source:** [PLAN.md:898-946]

---

## S08 · Tool runtime

**High level:** 6 builder tools and a few read-only ones, served two ways: a private stdio MCP server per `claude -p` session, and a Go tool loop for API links. No native Bash or Edit anywhere.

**Fits with:** every call passes S12 first; `run` executes inside S13; S22 serves file slices; S16 is the only route to the event bus.

**Internals:**
- **Harness loop (API links):** tools on `auto`, a text-only reply ends work, 1 final call returns the result schema; schema failure gets 1 re-prompt; the agent's gate (`done_when`, `lint_backlog`) feeds back into the same session twice, then `check_fail`.
- **Context limit:** Go counts tokens per turn; at 80% old tool outputs shrink to stubs (full text in `<session>/out/`); never sends past `ctx`.
- **Tool facts:** 4 KB cap per tool except `read` (2,000 lines); `run {class, args[]}` with no shell; `edit` exact unique replace; `write` new files only; repeat reads return "unchanged".
- **MCP facts:** `MCP_TIMEOUT` 30 s to answer `initialize`; S13's 10-minute `run` timeout is the only one that fires; `CLAUDE_CODE_RETRY_WATCHDOG` stays off so S05 can fail over.
- **3 inline hooks only:** `tool.pre` (guard), `tool.post` (format after `edit`), `session.end` (WIP commit).

**Commands owned:** `internal tools --agent --session`.

**Config keys:** none.

**Files owned:** `tools/{tools,mcp,files,run,git,submit,extra}.go`, `provider/loop.go`.

**Decided for you:** `edit` and `write` stay separate; `check_task` and `submit_result` dropped; `run` returns the capped result only.

**Source:** [PLAN.md:948-986]

---

## S09 · Skill loader

**High level:** 34 `standards-*` skills plus 5 workflow skills, embedded in the binary, indexed by name, globs and repo markers. Every relevant allowlisted skill preinjects; any can be pulled on demand. The repo's `.claude/` is never read.

**Fits with:** S07 allowlist per agent; S11 places standards as prefix part 4; S20 compiles `Never`/`Always` lines into reviewer rule IDs; S12 holds the steering phrases.

**Internals:**
- **Strict frontmatter v1:** `name, description, globs, markers, sections`; unknown key, duplicate heading or a `sections` entry with no heading fails the gate.
- **Bounds:** 6,000 tokens per skill; no preinject or pull cap.
- **Integrity:** `embed/skills/index.json` with a SHA-256 per skill, verified on read; rendered as `## Standards: <name> (<sha8>)`.
- **Steering scan:** a phrase from the `text` policy row fails a skill at the gate and warns on a repo addendum in `doctor`.
- **Order:** `always` first, then name order, so the prefix is byte-identical across runs.

**Commands owned:** `skill list|get` (`--agent`, `--section`).

**Config keys:** none.

**Files owned:** `agent/skills.go`, `embed/skills/**`.

**Decided for you:** no preinject budget; no pull limit.

**Gaps:** G-13 (steering phrase list is 6 by judgment).

**Source:** [PLAN.md:990-1023]

---

## S10 · Rules and output contract

**High level:** no rule text ever enters a worker prompt; the harness enforces rules where it acts (guard, schema, Check, review). Models return codes and facts; Go templates render every word a person reads, identically under `adhd` and `standard`.

**Fits with:** S01 renders `report`; S02 renders the orchestrator rules into the `AGENTS.md` block; S07 result schemas use its code list; S24 event `reason`s are its codes.

**Internals:**
- **Template pipeline:** `text/template` with `missingkey=error` and 6 functions only; one Go struct per template; facts → fact checks (SHA, path, task ID) → escape → template → 80-column wrap → color.
- **Golden fixtures:** every template renders its fixture under both profiles at the gate and compares bytes.
- **Tables:** over 5 rows → top 5 plus `N more`; a cell over 45 characters moves to a labeled line.
- **Code list:** beta 5's blocks plus `cancelled, paused, integration, epic_gate, dependency, flaky, tester, config_tampered, locked, regression, lint, disputed, crash` and the tester's item codes.
- **No leaks:** the addendum strips the Komodo block; a worker `agent.json` listing an orchestrator skill fails the gate; `--setting-sources ""` on CLI links.

**Commands owned:** none.

**Config keys:** `output.profile` (adhd), `output.turn_end_summary`, `output.color`.

**Files owned:** `agent/{rules,render}.go`, `embed/rules/orchestrator.md`, `embed/output/**`; the event part of `embed/state.schema.json` (owned by S29).

**Decided for you:** none new; the pipeline and fixtures are additions.

**Source:** [PLAN.md:1025-1097]

---

## S11 · Context and brief builder

**High level:** assembles each session's brief in a fixed order, most stable first, so the provider cache reuses the longest identical prefix: tools, role, repo context, standards, task. One ceiling: half the link's measured context.

**Fits with:** S07 slots, S09 standards, S05 measured `ctx`, S22 slices, S21 cache breakpoints at the part boundaries, S25 `repo_map`, S19 `baseline`, S20 `lens_rules`.

**Internals:**
- **20 slots**, required or optional per agent (`drafts/README.md` at `fdf4f61f`); an empty required slot fails the brief.
- **Blind reviewer and tester:** both get engine facts only (task list at the base commit, diff, Check results); never builder prose.
- **Repo context:** `AGENTS.md` minus the Komodo block, read from the base commit.
- **Trimming:** Go signature trimming, delta-only diffs on re-review, ANSI and build noise stripped.
- **Over the ceiling:** the task goes to the next link in the chain that fits, else blocks with `context`; nothing is cut to fit.

**Commands owned:** none.

**Config keys:** none.

**Files owned:** `agent/{brief,trim}.go`, `sessions/<id>/brief.md`.

**Decided for you:** one ceiling per link replaces 4 per-slot caps.

**Source:** [PLAN.md:1099-1120] and `drafts/README.md` at `fdf4f61f`

---

## S12 · Guardrails

**High level:** every tool call is checked in-process before it runs, fail-closed, against rows loaded from `embed/policy.json` and scoped per agent. Refusals name the rule and the alternative; the 3rd refusal under one rule ends the session.

**Fits with:** S08 calls it as `tool.pre`; S13 is the real boundary for anything `run` executes; S27 renders Part C tables from the same file; S09 scans skills against its `text` row.

**Internals:**
- **Row fields:** `id, scope (agent names | writers | runners | all | orchestrator), kind (tool | path | run | text), pattern, action, message, alternative`.
- **Rows:** G1 unlisted tool, G2 outside the worktree, G3 protected paths, G4 secrets, G5 trailers, G6 refusal limit, R1 declared run classes only, B1 builder writes only to `files`; O1–O10 are orchestrator text rules.
- **Scoped, no dead rows:** the scout's table has 4 rows, the builder's 8.
- **Security rows are never config:** only `guard.refusal_limit` is tunable.
- **One human path:** `needs_scope` (B1) is the only refusal a person or orchestrator can turn into an allow.

**Commands owned:** none.

**Config keys:** `guard.refusal_limit` 3 (hard 5).

**Files owned:** `safety/{guard,rows,paths,shell}.go`, `embed/policy.json`.

**Decided for you:** rows as `policy.json` data with scopes; B2 moved to `doctor backlog` lint; exactly 3 hooks.

**Gaps:** G-13.

**Source:** [PLAN.md:1124-1161]

---

## S13 · Sandbox and process control

**High level:** three layers, OS sandbox last: the tool surface, process limits, then seatbelt (macOS), bwrap (Linux, WSL) or Docker (native Windows) around every `run`. Network off except loopback; writes only to the worktree, session temp and Komodo's own language caches.

**Fits with:** S08 `run`, S04 detected commands and setup at Intake, S19 and S20 test runs, S15 worktrees, S18 sweep for cache cleanup.

**Internals:**
- **Profiles from `agent.json`:** read-only agents' `run` may write the worktree for tests; the engine resets tracked files afterward and fails that review if any changed.
- **Caps:** `run` timeout 10 min; 512 processes and 8 GB via cgroups or Docker; none on macOS and `doctor` says so.
- **Language caches:** per repo under `~/.komodo/cache/lang/<repo-id>/`, never your own `~/.npm` or `~/.cargo`; writable; capped at 10 GB.
- **Test sandboxes:** the tester is blind by profile (cannot read the builder's worktree); private loopback on Linux and Docker; macOS serializes `test`-class runs per repo; service-backed tests warn through the baseline; items may declare `needs:`.
- **Copy mechanics:** APFS `cp -cR` clone (proven, 1.2 GB in 0.00 s), Linux `--reflink=auto`, NTFS Go byte copy with junctions, Docker host copy then bind; symlinks kept as links; Rust shares a per-group target dir; Python reruns `uv sync`.
- **Docker runtime:** one container per worktree from the full official image, setup with network on then disconnected, each `run` a `docker exec`, `git-read` on the host.

**Commands owned:** `doctor --sandbox <cmd>`.

**Config keys:** `sandbox.network` deny, `sandbox.max_procs` 512, `sandbox.max_mem_gb` 8, `sandbox.image`, `sandbox.lang_cache_gb` 10.

**Files owned:** `safety/{proc*,sandbox*}.go`.

**Decided for you:** the Rust shared target dir.

**Gaps:** G-08 to G-12 (bwrap loopback, reflink, Windows links, Docker copy time, Rust target reuse), all phase 2 spikes.

**Source:** [PLAN.md:1163-1224]

---

## S14 · Orchestration engine

**High level:** one `Drive` goroutine per group loops over the pure `Next()`; groups form a DAG from the backlog; a machine-wide session pool and a file-based work queue coordinate across repos and engines. Build runs one fresh session per task; Verify runs reviewer and tester at once with mutual cancellation; landings are one at a time with a Check on the epic tip.

**Fits with:** §1 is its contract; S15 leases and worktrees; S16 sessions; S17 enqueues jobs; S18 bounds every loop; S19, S20, S26 are its stages.

**Internals:**
- **Stages:** Intake (0 tokens: lint, hash the card, lease, cut worktree, setup) → Build → Check → Verify → Ship, plus Paused and Blocked; a failed Check or Verify returns to the task's own builder session.
- **Concurrency:** one `Drive` per group owns its state; one goroutine per session feeds a 256-buffered channel that blocks rather than drops; cross-group state is create-only or renamed files; every goroutine runs under a panic helper.
- **Work queue:** `state/queue/<repo-id>.<kind>.<id>.json`; taking a slot deletes the file then writes the lease, so a crash between leaves a re-enqueueable item.
- **Pick order:** `priority: high`, then repo fair share, then longest chain by task count, then most groups unblocked, then ID.
- **Pool:** floor 1, ceiling 6 (hard 16); grows by 1 after a clean end while memory allows; halves on a rate limit; builder, reviewer and tester take slots; every session also takes a link slot.
- **Convergence:** join rule for multi-dependency groups; integration break sends the last-landed group back to Build; the epic gate reviews the whole diff and routes findings to the group that last changed the file.

**Commands owned:** `run [group] --stage`, `status` (with `--wait, --watch, --explain, --metrics`), `resume`, `stop`, `abandon`.

**Config keys consumed:** `limits.pool_min/max`, `limits.link_sessions.<link>`, `limits.group_*`.

**Files owned:** `engine/{engine,stage,intake,build,verify,pool,queue}.go`. The group's events and snapshot moved to S29's group state.

**Decided for you:** none new; the Verify race rule and the queue are additions to approved S14.

**Gaps:** G-19 (10,000-event replay under 1 s is a target).

**Source:** [PLAN.md:1228-1286]

---

## S15 · Workspace and leases

**High level:** one detached worktree per group under `~/.komodo/wt/<repo-id>/`, one create-only lease file per group in a machine-wide table, and a monotonic fence that git itself enforces on every ref move, push and landing.

**Fits with:** S14 holds the lease from Intake to landing; S18 action keys carry the fence; S26 landings refuse on a stale fence; S23 shows holders.

**Internals:**
- **Lease:** `~/.komodo/leases/<repo-id>/<group>.json` with run ID, pid, host, clone path, `fence`, times; renewed every 5 min; expires 2 h after the last renewal; stale ones are renamed aside before takeover.
- **Fence:** `<group>.fence` rises on every take; `git update-ref <ref> <new> <old>` is the compare-and-swap, with `fence=<n>` in the reflog; a refused update emits `lease.lost` (proven: stale write exits 128).
- **Saved work:** hidden ref `refs/komodo/groups/<group>` moves to every commit, never pushed, deleted on landing.
- **Shutdown:** stop agents 30 s each, WIP-commit, free leases, write `state.json`, exit; 60 s then force kill; a parent guard repeats this if the engine dies hard.
- **Sweep at start:** orphans, dead leases, gone clones, dead runs' tester worktrees and containers; a worktree idle 3 days is WIP-committed and removed.

**Commands owned:** none directly; `stop`, `resume`, `abandon` route through it.

**Config keys:** none.

**Files owned:** `engine/{worktree,shutdown}.go`, `~/.komodo/wt/`, `~/.komodo/leases/`.

**Decided for you:** none new.

**Source:** [PLAN.md:1288-1310]

---

## S16 · Session supervisor

**High level:** spawns one provider session per task with its brief, tool server and sandbox profile, then watches it by events only: stream lines, tool calls and `api_retry` lines are liveness. It emits `session.*` events; `Next()` decides what happens.

**Fits with:** S05 streams, S13 kills by tree, S18 retries, S23 ledger rows, S14 `Drive` channel.

**Internals:**
- **Windowed timer per task:** the session wall is the task's window; at 80% one fixed line is injected over stdin; the clock stops while paused.
- **Idle:** 10 min with no line and no running tool; one nudge, 5 min grace, then stop.
- **Stop ladder:** SIGINT (ends the turn, keeps its result), 30 s, SIGTERM, 60 s, tree kill; context cancel on API links.
- **15 exit reasons:** `done, needs_scope, check_fail, no_result, schema_fail, timeout, idle, refusals, budget, provider, billing, paused, pause_timeout, cancelled, crash`.
- **One 15 s timer per session** checks wall, idle and the warning; supervision costs under 1% of a core at 6 sessions.

**Commands owned:** `status --session <id>`.

**Config keys consumed:** `limits.session_wall_min` 60, `limits.session_idle_min` 10, agent `retries`.

**Files owned:** `engine/session.go`, `sessions/<id>/{brief.md, request.json, stream.jsonl, messages.jsonl, result.json}`.

**Decided for you:** the 5 held items: nudge text, stream-json stdin, retries from `agent.json`, API idle via S06, `paused` exit reason.

**Gaps:** G-14 (a nudge reaching a turn stuck in a tool call is best effort).

**Source:** [PLAN.md:1312-1345]

---

## S17 · Scheduler and job templates

**High level:** no daemon. `komodo jobs tick` is one idempotent engine step: enqueue due jobs, fire due pause timers, drive the pool once. An OS scheduler entry calls it every 5 minutes.

**Fits with:** S14 queue, S15 lease for overlap, S05 `wait_until` timers, S18 for a tick with no live engine.

**Internals:**
- **Template:** `{name, agents[], slots{}, every}`; no limits inside; strict decode like `agent.json`.
- **Overlap:** a running job holds lease `job-<name>`; the next tick's item is refused as a duplicate and logged.
- **Queue position:** `priority: normal`; fair share treats each job as its own repo.
- **Counters:** `jobs/state.json` holds `attempt, next_at, last_fail_hash, running`; retries 3 with backoff 30 s, 2 m, 8 m ±20%; then Blocked and a wake.

**Commands owned:** `jobs list|show|run|pause|resume|tick` (`run --ignore-window`); `install --schedule`.

**Config keys:** none.

**Files owned:** `engine/schedule.go`, `host/schedule.go`, `embed/jobs/`, `~/.komodo/jobs/*.json`, `jobs/state.json`.

**Decided for you:** none shipped; one example template.

**Source:** [PLAN.md:1347-1384]

---

## S18 · Error recovery

**High level:** every failure has a detector, an action, a bound and a final state; nothing loops without a counter. The loss bound: any single failure loses at most one model turn and no committed edit.

**Fits with:** every row is one `Next()` case (S14); detectors are S05, S06, S08, S12, S16, S19, S20, S26; the action table is the §1 outbox.

**Internals:**
- **27 failure rows** from transient HTTP to budget spent, each with a bound (chain length, `retries` 2, `limits.returns` 3, `limits.task_attempts` 6) and a block reason from a fixed enum.
- **Crash safety:** resumes a group through S29's group state `Load`, which owns the state sync, the 50-event snapshot rate and replay.
- **9 action kinds with idempotency keys:** `spawn` (UUID), `wip_commit`/`commit` (group, fence, tree), `cancel`, `setup` (lockfile hash), `check` (tree, command), `land` (group, fence, merge base), `open_pr` (epic, head), `scope_change` (sorted paths), `notify` (seq).
- **Resume:** provider handle first (`--resume <uuid>` with `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1`, or `messages.jsonl`), else a fresh session with the WIP diff and last `summary`, logged `resume_lost`.
- **Paused is not Blocked:** paused resumes itself on a timer; blocked needs a person or the orchestrator; a group blocked 3 times for one reason is flagged `needs redesign`.

**Commands owned:** `resume <run> --from <stage>` behavior; the startup sweep.

**Config keys consumed:** `limits.task_attempts`, `limits.returns`, `limits.pause_max_min`.

**Files owned:** `engine/{limits,recover}.go`.

**Decided for you:** the 3 deferred items: WIP commit at every session end, crash contained to its group, `resume` continues sessions.

**Source:** [PLAN.md:1386-1453]

---

## S19 · Check (ground truth)

**High level:** 0-token mechanical gates after Build and before Ship: rerun every `done_when`, enforce scope, scan secrets, measure patch coverage, diff tests against a baseline at the base commit, and run only the impacted packages for Go.

**Fits with:** S13 runs it in the sandbox; S22 caches by tree hash; S20's tester reads the baseline; S14 reruns it on the epic tip after each landing.

**Internals:**
- **Baseline:** Intake records the `test` result at the base commit; a test that passed at base and fails at head is a `regression` and blocks; failing at both is pre-existing and warns.
- **Impact set:** `go list -deps` reversed (proven: 13 of 40 packages for one change); other languages run the full suite; the epic gate always runs everything.
- **Flake rule:** any failing test is rerun twice alone; two more failures confirm, a split reports `flaky` without blocking.
- **Scope requests:** `needs_scope` ends the session with a WIP commit; the orchestrator answers `scope approve|deny` after 2 engine denials (protected paths, paths in another live group); the same session resumes (your decision, G-15).
- **Coverage:** `check.patch_coverage` 80 on changed lines; Go blocks, others warn.

**Commands owned:** `scope approve|deny <group>` (`--paths, --reason`); `run --stage check`.

**Config keys:** `check.patch_coverage` 80 (hard 100).

**Files owned:** `quality/check.go`, `cache/checks/`.

**Decided for you:** coverage 80 blocking for Go only.

**Gaps:** G-23 (enterprise scope policy, later minor).

**Source:** [PLAN.md:1457-1480]

---

## S20 · Review and evidence

**High level:** the reviewer reads cold through scoped rule catalogs (10 lenses) and returns one verdict per task; the tester proves acceptance items blind in its own worktree. Both run in Verify at once; findings block only with evidence Go can find in the diff.

**Fits with:** S11 feeds engine facts only; S09 standards compile to `std.<skill>.<n>` rules; S23 keeps the precision ledger; S14 handles the race.

**Internals:**
- **Lenses as data:** `embed/lenses/<lens>.json` rules `{id, severity, applies{globs, markers}, check, evidence, source}` plus a `.md` body; a lens runs only when a changed file matches `applies`.
- **10 lenses:** `correctness, security, spec, contract, concurrency, platform, config, readiness, quality, economy`; the benchmark is Google's review guide plus SWE-bench Verified failure classes.
- **Per-task verdicts:** `met | not_met | brief_incomplete` per task ID exactly once; the tester returns `confirmed | not_confirmed` per task and pass/fail per item; Go cross-checks the two.
- **4 gates, 3 of them Go's:** lint and coverage, success, regression (0 tokens), then the tester's blind pass.
- **Tester restart:** when Go's 2 reruns pass an item the tester failed, the tester restarts once with Go's output and must return `env | wrong_command | flaky | real_failure`; a second dispute blocks with `tester` (a harness fault).
- **Precision ledger:** submitted, verified, fixed, `disputed` per rule and model; a rule under 50% verified is listed by `doctor`, under 30% for 60 days is a plan change.
- **Re-review:** delta only; cached findings cost 0 tokens; the tester reruns every item.

**Commands owned:** `run --stage verify`.

**Config keys consumed:** `mode` (economy runs one merged lens).

**Files owned:** `quality/{review,qa,evidence}.go`, `embed/lenses/`, `cache/review/`.

**Decided for you:** 10 lenses; `blocker` and `major` block, `minor` is counted.

**Gaps:** G-16 (lens catalog content; you review the security lens).

**Source:** [PLAN.md:1482-1526]

---

## S21 · Token and resource management

**High level:** budgets are bounds against runaway loops, not cost controls; USD only warns. Savings come from a stable prefix, trimming, per-task sessions and provider prompt caching with 4 breakpoints placed at S11's part boundaries.

**Fits with:** S04 keys, S05 effort and thinking fixed per agent, S11 prefix order, S23 metrics.

**Internals:**
- **Budgets:** group wall (tasks × session wall + 2 × session wall), agent wall/idle/retries, `limits.task_attempts`, the pool and link slots, mode.
- **Cache breakpoints:** (1) tools plus role 1 h, (2) repo context 1 h, (3) standards 5 min, (4) task 5 min; 512-token minimum; reads at 0.05× on Opus 5.5 and Sonnet 5.5; cached reads do not count toward input-per-minute limits.
- **Token cap:** `limits.group_tokens` 2M counts input, output and cache writes; cache reads are reported only.
- **Headline metric:** tokens and USD per accepted group plus cache hit rate per agent; under 50% over 20 API sessions fails a gate fixture.

**Commands owned:** `status --metrics` content.

**Config keys consumed:** `limits.group_tokens` 2,000,000 (hard 10M), `mode` thorough.

**Files owned:** `ops/budget.go`, cache settings in `agent/brief.go`.

**Decided for you:** thorough as the default mode.

**Gaps:** G-07, G-17 (50% hit-rate fixture needs real sessions).

**Source:** [PLAN.md:1530-1558]

---

## S22 · Cache store

**High level:** a content-addressed store outside every worktree, keyed by `sha256(inputs + binary version)`, verified on read, 256 MB LRU per repo. Model responses are never cached.

**Fits with:** S09 bodies, S11 slices and briefs, S19 checks and baseline, S20 findings, S14 setup results, S25 repo map.

**Internals:**
- **10 entry kinds:** skill index and bodies, repo profile, file slices, diffs, briefs, check results, review findings, setup results, test baseline, repo map and outline lint.
- **Layout:** files named by key under a 2-level fan-out; one `stat` per lookup; in-memory LRU index per process.
- **Poison-proof:** outside G2's reach; a hash mismatch deletes the entry.

**Commands owned:** `cache stats|clear --kind`.

**Config keys:** `cache.max_mb` 256 (hard 4096).

**Files owned:** `core/cache.go`, `<git-common-dir>/komodo/cache/{skills,context,checks,review,setup,map}/`.

**Decided for you:** none new.

**Source:** [PLAN.md:1560-1590]

---

## S23 · Runtime observability

**High level:** one ledger row per session, a read-only copy of every event, and two rebuilt views (`tasks.json`, `groups.json`) that form the workflow ledger: who holds which group and task, under which lock, lease and fence.

**Fits with:** a subscriber of S24; feeds `status`, the sweep and the orchestrator; S20 precision data; S21 metrics.

**Internals:**
- **Task list lock:** Intake hashes `TG.md` and `tsk-*.md` into `card_hash` and emits `lock.taken`; `add backlog --to`, `plan --epic` and hand edits refuse with `locked` and the run ID until `lock.released`.
- **Task claims:** `queued → claimed → done | returned | blocked`; `task.claimed` at `session.start`, `task.released` at `session.end`; a dead session's task is released with `crash` at the sweep.
- **Scorecard:** first-pass rate, returns per group, finding precision, tester false-fail rate, tokens and USD per accepted group, cache hit rate; `doctor` suggests a weight change when a light model beats the standard one over 20 groups.
- **Redaction:** keys and secret matches masked before any write.
- **Retention:** 30 days or 100 MB, oldest first; open, paused or blocked groups are never trimmed.

**Commands owned:** `status` views (`--watch, --wait, --session, --explain, --backlog, --metrics, --all`).

**Config keys:** `ledger.retention_days` 30 (hard 365).

**Files owned:** `ops/ledger.go`, `ledger/{sessions.jsonl, events.jsonl, tasks.json, groups.json}`.

**Decided for you:** no OpenTelemetry in beta 6.

**Source:** [PLAN.md:1592-1620]

---

## S24 · Event bus

**High level:** the envelope, catalog and in-order delivery of §1, in-process with no broker; S29's group state writes and replays the events. Exactly 3 subscribers that never change group state, each writing only its own derived kind through S29: the ledger, the wake file, the `status --watch` renderer.

**Fits with:** S14 `Drive` is the only emitter path; S23 subscribes; S01 `status --wait` watches the wake file.

**Internals:**
- **Wakes:** `group.blocked`, `group.paused`, `run.done`, `run.stopped` touch `state/wake/<run>`; `--wait any` adds `stage.enter`; the waiter reads the state tail after subscribing so no event is missed.
- **Delivery:** synchronous, `seq` order, after `Next()` returns; a subscriber over 5 ms or panicking is logged and dropped for the run.
- **No hooks on the bus:** the 3 inline hooks are S12's; repo hooks wait for a later minor and will be subscribers, never gates.

**Commands owned:** `status --wait` mechanics.

**Config keys:** none.

**Files owned:** `ops/{bus,wake}.go`, `state/wake/`.

**Decided for you:** none.

**Source:** [PLAN.md:1622-1645]

---

## S25 · Backlog and planning

**High level:** input is docs and context, output is a graph. The planner fills blanks inside a mechanical grammar; Go lints structure, derives the dependency DAG from file globs, enforces sizing, and writes files only when lint passes.

**Fits with:** S14 reads the DAG and priority; S23 locks the card; S20's tester proves the items; S11 `repo_map` slot; S12's former B2 lives here as lint.

**Internals:**
- **Grammar:** `docs/backlog/epic-N/tg-N.M/{TG.md, tsk-*.md}`; `TG.md` gains `subsystem:`, `touches:`, `priority:`; `tsk-*.md` gains `estimate_lines:`.
- **Two passes:** pass 1 (heavy) returns the outline: groups, goals, `touches`, dependencies; pass 2 runs one standard session per group in parallel for tasks and items; a lint failure goes back into the session twice, then `lint`.
- **DAG from `touches`:** overlapping globs must be ordered; the planner's stated dependency is kept when it agrees, else lint names both groups and the file.
- **Sizing lint:** 5 files and 300 estimated lines per task, 8 tasks per group, chains of 4; real line counts feed back as warnings.
- **EARS items:** `WHEN <event> THE SYSTEM SHALL <response>` with optional `cmd:` and `needs:` lines.
- **`repo_map`:** symbol graph via `go/ast` or `embed/langs/<lang>.json`, ranked by inbound references, fitted to 10% of the brief ceiling.
- **Deltas, never rewrites:** `plan --epic N` touches only unlocked groups.

**Commands owned:** `plan <goal> --from --epic`, `add backlog`, `doctor backlog`.

**Config keys:** none.

**Files owned:** `repo/{backlog,lint,graph,repomap}.go`, `embed/rules/backlog.md`, `embed/langs/`.

**Decided for you:** EARS items; the two-pass planner; B2 as lint; the sizing caps.

**Gaps:** G-18 (definition patterns for TS, Python, Rust).

**Source:** [PLAN.md:1649-1677]

---

## S26 · Ship: git, PR, release

**High level:** only the harness writes git. Each group lands on `komodo/<epic>/<group>`, merges into `komodo/<epic>` per `ship.mode`, fenced by the lease; a person merges only the epic PR.

**Fits with:** S15 fence, S18 `land` and `open_pr` keys, S19 Check on the epic tip, S10 PR body template, S03 verifies what `release` signs.

**Internals:**
- **Ship (0 tokens):** merge the epic branch into the group branch (no rebase), rerun Check, land; one landing at a time.
- **`ship.mode`:** `group` opens a PR per group into the epic branch and merges it at once; `epic` merges `--no-ff` directly.
- **Never:** force push, `+refspec`, trailers, session links, critical refs.
- **Release:** version, tag, cross-builds, checksums, ed25519 signature, changelog from `1.0.0-beta.6`.

**Commands owned:** `release <version> --notes-file`, `run --stage ship`, `sync` after a merge.

**Config keys:** `ship.mode` group.

**Files owned:** `repo/{ship,git,pr,release}.go`.

**Decided for you:** `ship.mode` default `group`.

**Source:** [PLAN.md:1679-1698]

---

## S27 · Gate and self-checks

**High level:** `komodo gate` is the build's conscience: vet, tests, cross-builds, import direction, strict decodes, guard table, event catalog, docs drift, S-ID maps, generated artifacts, timing, and the no-silent-gaps rule.

**Fits with:** every subsystem's generated artifact: `policy.json` tables (S12), `state.schema.json` (§1, S29), skill hashes (S09), standards rule IDs (S20), golden fixtures (S10), the action table (S18).

**Internals:**
- **No silent gaps:** every `[NOT CHECKED]` or `G-NN` tag in `docs/lld.md` must name a backlog task; this is how the gap register survives `PLAN.md`'s removal.
- **Timing:** short verbs under 50 ms, gate under 3 minutes locally, 10,000-event replay under 1 s.
- **`--fast`** skips cross-builds and timing; `--install` places a dev build; `--install-hooks` installs git hooks in this repo.

**Commands owned:** `gate --only --fast --install --install-hooks`.

**Config keys:** none.

**Files owned:** `repo/gate.go`, `safety/table.go`.

**Decided for you:** gate under 3 minutes.

**Gaps:** G-19.

**Source:** [PLAN.md:1700-1720]

---

## S29 · State manager (core pillar)

**High level:** one schema for every state record and event, and one door to it. An in-process service, started once per `komodo` process, is the only code that reads or writes runtime state; it fills in each record's envelope and defaults and refuses any write that breaks the schema or an invariant.

**Fits with:** S14 `Drive` writes events and the group snapshot through it, and `Next()` still decides every change; S15, S05, S17 and S23 write their own kinds through it; S24 `status --wait` uses its `Watch`; S27 fails any other package that writes a state path; S03 relies on its migrations to read older state.

**Internals:**
- **Envelope:** `v, kind, key, rev, ts, writer`, all set by the manager; an event adds `seq` and `crc`.
- **Write checks:** types, required fields and enums; `seq` has no gaps, `epoch` and `fence` never fall, `rev` must match (compare-and-swap), and the caller must own the key.
- **Group state (`state/groupstate.go`):** a group's events, snapshots and replay as one unit: `Open`, `Append`, `Snapshot`, `Load(next)`, `Tail`. `Next()` stays in the engine and is passed in.
- **Primitives:** `Append` (state files), `Replace` (snapshots and timers, by temp file and rename), `Claim` (create-only: queue, leases, slots) and `Remove` (after the owner and `rev` check).
- **Torn last event line:** dropped with one `state.truncated`, never quarantined, so a crash mid-append doesn't block a group.
- **Writers:** each kind lists its writers (S14, S15, S17 and S18's sweep for the queue and leases, for example); the manager refuses any other.
- **Bad record:** moved aside as `.bad-*`; its group blocks with `state_invalid`.
- **Out of scope, 6:** `config.json` (S04), `cache/` (S22), git refs and worktrees (S15), job templates (S17), binaries and `release.key` (S03, S26) and session temp (S13).

**Commands owned:** none; `help --json` serves `state`.

**Config keys:** none.

**Files owned:** `state/{manager,groupstate,schema,kinds,validate,migrate}.go`, `embed/state.schema.json`, `runs/`.

**Decided for you:** in-process with no daemon; the 6 exclusions; quarantine rather than repair; the group snapshot field list; the pause timer moved from the ledger to a `timer` record.

**Gaps:** G-24.

**Source:** [PLAN.md:1750-1805], working tree; the other cards cite commit `ba4ff823`

---

## Approval sheet

| Card | Status |
|---|---|
| §1 | pending |
| S01–S04 | pending |
| S05–S08 | pending |
| S09–S13 | pending |
| S14–S18 | pending |
| S19–S24 | pending |
| S25–S27 | pending |
| S29 | pending |

Update this sheet as you approve; each approval is also a Review changes row in `PLAN.md`.
