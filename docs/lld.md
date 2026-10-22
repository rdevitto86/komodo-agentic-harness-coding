---
service: tool:agentic-harness-coding
domain: ai
type: lld
status: active
owner: R. DeVitto
depends-on: []
---

# Low-level design — komodo-agentic-factory-coding

How each part works. A builder reads one section of this file through a task's `context`, such as `docs/lld.md#interfaces`; nobody reads it whole. Headings carry no numbers, so a citation survives a reorder. A requirement's number is cited by its `REQ-n`, never copied. A contract, manifest, schema, or migration is linked, never restated. A section that does not apply says "Not applicable."

**Status:** Accepted, 2026-09-25, for 1.0.0. The mechanics behind the decisions in `decisions/`. Values marked "starting value" are tunables that no requirement sets; the first eval recalibrates them.

## Data model

### Task groups

A task group is 1 to 12 tasks (REQ-8), about the size of one engineering story: typically 2 to 6, such as the core change, its tests, validation and deployment. The group's task list is the builder's brief and the reviewer's yardstick. Each task is a checkbox that only the conductor ticks, after the task's checks pass (REQ-10).

````markdown
## [TG-04.1] Tokens report their expiry [P: H] [READY]

```yaml
type: feat
version: 1.4.0
epic: EPIC-04
depends_on: []        # groups whose unmerged work this one needs; its PR stacks on theirs
```

- [ ] **TSK-04.1.1** Tokens know when they expire
  - files: `internal/token/`
  - accept: Expired is true at or after ExpiresAt
- [ ] **TSK-04.1.2** An expired token gets a 401
  - files: `internal/api/auth.go`, `internal/api/auth_test.go`
````

A task needs a title and its files (REQ-9). `accept` lines are optional; they feed the correctness lens along with the PRD. Hand-written `checks` add to the derived ones. The group heading carries one of three statuses: READY, BLOCKED or REFINEMENT; the conductor tracks finer states in [Group states](#group-states). The exact grammar lands with the ingest rewrite, and `komodo lint` enforces it.

### The backlog

The backlog is a committed plan that keeps agents in sync across workloads (decision 0004). It is one file per group in `docs/backlog/`, named `<group-id>-<slug>.md`, and each file carries its epic's ID. People and the orchestrator write groups through `/plan` or `komodo add`, and plan changes land through pull requests like any other change. The conductor reads groups from the base branch, ticks boxes on each group's own branch, and keeps live progress in run state. A group's PR therefore shows the code and the completed task list together. A group's PR never edits `CHANGELOG.md`; only a release writes it, describing what the version ships. There is no index and no archive: `komodo backlog` lists the open groups.

Cleanup is mechanical (REQ-46):

| Leftover | Removed when |
|---|---|
| An epic's group files | The PR of the epic's last open group deletes them; a group with no epic deletes its own file |
| An epic's files that outlived it | `komodo sync` opens a cleanup PR, for example when an epic's last two groups finished together |
| A group's detached worktree and its `refs/komodo/<branch>` tip | Ship finishes, or the next run starts, for merged or abandoned groups; Komodo never deletes a branch under `refs/heads/` (decision 0012) |
| Any `.komodo/wt` worktree whose work is safe on origin | A person's session starts and sweeps in the background: clean, unleased, no open run, and either merged or idle 24 hours with every commit on origin |
| A group's remote branch | The forge deletes head branches on merge; `komodo doctor --remote` checks that setting |
| Run folders | Only the last 10 runs are kept, a starting value |
| Anything else | `komodo doctor` flags an ended epic's files, and any worktree or branch with no group |

A PR closed without merging gets a blocker note, and its group stays open.

### Blocker notes

When the orchestrator can't settle an escalation, the conductor stops the group and sets it BLOCKED. It writes a note under the group heading on the group's branch, then publishes the branch as a draft PR labelled `status/blocked`, so every developer and agent sees it (decision 0005):

```markdown
> **Blocked** 2026-09-25 14:02, run r-0142, at Review.
> - TSK-04.1.2: the reproducer needs a clock the API can't inject; adding one is outside this task list.
> - Needs: a decision on adding a clock parameter to `auth.New`.
> - Saved: WIP commit `3f2a9c1` on `feat/tg-04-1-token-expiry`.
```

A person, or the orchestrator on their word, edits the group on that branch: answers the question, changes a task, or splits the group, then sets it READY. `komodo resume` continues from the branch, feeds the edited text to the resumed builder, and removes the note.

### Group states

The conductor writes each state to `state.json` before starting its work, so a resumed run continues from the last state.

| State | Meaning | Moves to |
|---|---|---|
| Ready | Waiting for a slot | Building |
| Building | The builder session is running | Checking |
| Checking | The conductor is rerunning every check | Reviewing; or Repairing when a check fails |
| Reviewing | The lenses are running | Repairing when findings are verified; otherwise Preparing |
| Repairing | The builder is resumed with a fix list | Checking |
| Preparing | Commit, hooks, rebase and integration | Shipping; or Repairing on a conflict or an integration failure |
| Shipping | Push, draft PR and labels | Shipped |
| Shipped | The PR is open and waits for a person to merge | Removed after the merge |
| Escalated | Waiting on the orchestrator | Back to the state it left, or Blocked |
| Blocked | Stopped, with a blocker note on its branch and a draft PR labelled `status/blocked` | Ready, once a person edits the group |

### Group cards

`komodo ingest` compiles each READY group into `.komodo/queue/<group>.json`, with zero tokens and a stable hash (REQ-7).

| Field | Derived from |
|---|---|
| Task list | The group's checkboxes, in order |
| Files | Globs and directories expanded against the tree; new files allowed where the parent exists |
| Checks | Detection per language touched. For Go: build, vet and test of each touched package. Hand-written checks are added, never replaced. |
| Context pack | File bodies, signatures of imported packages, callers of changed symbols, neighbouring tests, the spec sections the group cites, and the repo's rules; each capped |
| Tier | The role's tier; `heavy` only when the group asks for it |
| Size | Tasks, files, packages and brief bytes |
| Base | The group's epic branch, `feat/<epic's version>`, or the branch of a group named in `depends_on` (REQ-13, decision 0006) |

### Run state and metrics

Each run writes to `.komodo/runs/<run-id>/`, which is gitignored and local. Each run starts with fresh metrics.

| File | Holds |
|---|---|
| `state.json` | Each group's state, worktree, branch, last WIP commit, session IDs, open findings and time used |
| `line.jsonl` | One line per stage and session: run, group, stage, start, duration, turns, input, output and cached tokens, outcome (REQ-28); archived to `line.<run>.jsonl` when the run ends |
| `events.jsonl` | Escalations and how they were settled, stops, pauses for usage limits, and resumes |

### Results

Every model session returns JSON checked against its role's schema; the conductor rejects a result that fails it.

| Role | Returns |
|---|---|
| Builder | Per task: done or blocked, the checks it ran, and a question when blocked |
| Review lens | Findings: lens, rule ID, severity, file, line, evidence and a one-line fix instruction |
| Orchestrator, on an escalation | Exactly one action from [Escalations](#escalations) |

## Interfaces

### The `komodo` command

`komodo` is the conductor. The installer puts it on PATH. The `komodo` skill, which the orchestrator loads, is generated from `komodo help`, so an agent's reference never drifts from the binary.

| Command | Does |
|---|---|
| `komodo install` | Machine setup: links the binary onto PATH, installs the global orchestrator layer, runs doctor |
| `komodo init` | Writes the starter docs, an empty `docs/backlog/` and optional config into a repo; nothing it writes is required |
| `komodo run [group\|task] [--no-ship] [--dry-run] [--budget d]` | Preflight, then drives the next ready group, or the one named, to a draft PR under a budget; `--no-ship` stops it at Shipped-ready |
| `komodo status [--watch]` | The current run: groups by state, time used and blockers |
| `komodo resume <group>` | Continues a stopped or killed group from its saved session or WIP |
| `komodo ship <group>` | Finishes a group stopped before Ship |
| `komodo worktree add <branch>` | Cuts a detached worktree for ad hoc work, tracking `<branch>` (decision 0012) |
| `komodo pr create`, `komodo pr label` | Opens a pull request outside the line with its title checked and its labels applied: `@agent`, the scope `.komodo/labels.json` maps, the stage, and `branch/feature` off the default branch |
| `komodo check <task\|findings\|scope>` | The checks that hooks and agents call |
| `komodo backlog`, `komodo add <group> "<title>"` | Lists the open groups; adds a group or task |
| `komodo report` | Summarises the current run's metrics |
| `komodo sync` | Removes merged groups' worktrees and `refs/komodo` tips, opens a cleanup PR for an epic whose files outlived it, and updates the toolkit between runs |
| `komodo abandon <group>` | Removes a group's worktree and `refs/komodo` tip on purpose, and marks its file BLOCKED |
| `komodo lint`, `komodo doctor [--remote]`, `komodo eval` | Backlog grammar, machine and forge health, the golden suite |
| `komodo release` | This repo only: builds every platform, tests and publishes |

### Orchestrator commands

Each one is a thin skill that calls `komodo`. `/run` is the line's one entry; the conductor drives every
stage from there, no session relaying it. Ad hoc work is the orchestrator spawning its own default
agents outside the line, with no skill of its own (decision 0003).

| Command | Does |
|---|---|
| `/run [groups]` | Starts the conductor in the background and reports progress |
| `/status` | Shows groups by state, time used and blocker notes |
| `/plan <docs>` | Drafts task groups from the PRD and specs through the planner; they must pass lint |
| `/stop`, `/resume` | Stops or resumes groups |
| `/release` | This repo only: cuts a release through `komodo release` |

### The host contract

A host mount implements these operations (decision 0002). Claude Code implements every one.

| Operation | Contract | Claude Code, CLI 2.1.282 |
|---|---|---|
| Preflight | Is the host installed, pinned and logged in? | `claude --version`, `claude auth status` |
| Start | Run a role headless with a brief, tools, hooks, permissions, model and effort | `claude -p` with the flags below |
| Resume | Continue a session with new input | `--resume <session-id>` |
| Stream | Report turns, usage, cost and rate limits as they happen | `--output-format stream-json`, including `rate_limit_event` |
| Result | Return JSON checked against the role's schema | `--json-schema` |
| Stop | End the session and its whole process tree | The conductor kills the tree |
| Capabilities | Declare resume, sandbox, hooks and structured output | All four |

A host without resume gets a fresh session with the fix list and the saved diff. A host without a sandbox is treated like native Windows.

### How the conductor runs a Claude Code session

- **Where:** the process starts in the group's worktree.
- **What it loads:** the host's default config directory, which holds the login (decision 0001). `--setting-sources local` shuts out personal settings and instructions and the project's shared skills; the brief carries the repo's `AGENTS.md` rules (decision 0001). `--plugin-dir` adds the role's own plugin (its skills, hooks and agents). `--settings` adds the role's permissions and turns auto-memory off. `--strict-mcp-config` keeps MCP servers out.
- **What it may use:** `--tools` lists the role's tools. `--permission-mode dontAsk` refuses anything outside the allow list without a prompt (spike S2).
- **Which model:** `--model` and `--effort` come from the profile.
- **Limits:** `--max-budget-usd` on API billing, `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP=3`, `CLAUDE_CODE_MAX_TURNS` per role, and the conductor's clock, which kills the process tree at the limit. `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB` is never set: it keeps `GH_TOKEN` and overrides `dontAsk` (spike S8).
- **Environment:** the conductor removes forge credentials, sets `DISABLE_AUTOUPDATER=1`, and points `GOCACHE` and `GOTMPDIR` inside the worktree so a sandboxed build can write them (spike S2).

### Briefs

Every brief is built from slots in a fixed order: the stable slots first, so sessions share cached prompt prefixes. A slot over its cap is cut with a visible clip marker, never silently.

| Order | Slot | Cap |
|---|---|---|
| 1 | The role's template | Fixed per role |
| 2 | The repo's `AGENTS.md` rules | Doctor's always-on budget |
| 3 | The standards for each language the group touches | 6,000 bytes each |
| 4 | The lens checklist, for review lenses | Fixed per lens |
| 5 | The card's task list | 12 tasks (REQ-8) |
| 6 | The context pack | Per item, then in total; starting values |
| 7 | This round's input: a fix list, a person's edits, or the repair diff | Per round |

The same card and tree give the same brief bytes on every machine.

### The repo layer

A repo may commit `.komodo/`. Nothing in it is required, a malformed file is skipped with one line in the report, and nothing in it widens what the guard denies.

| Path | Adds |
|---|---|
| `context/*.md`, with a `paths:` glob list | Injected into any task whose files match |
| `standards/<name>.md` | Appends to a shipped standard of that name, or adds a new one |
| `skills/<name>/SKILL.md` | A new skill, or a "Repo overrides" section appended to a shipped one; rendered into the host's project directory as a gitignored copy |
| `commands.json` | Verify, compile, before-review and after-publish commands, each judged by the guard first; verify otherwise resolves by discovery |
| `policy.json` | Adds critical refs |

Precedence is defaults, then detection, then the machine overlay, then the repo, then the task; each layer can only add. The four orchestrator skills, `komodo`, `run`, `plan` and `escalate`, cannot be appended to by a repo (see #skills-and-scoping).

### Build

The builder works the tasks in order and runs `komodo check task` as it goes. It ends with a result per task. When it can't continue, it finishes as blocked with a question, and the conductor escalates (REQ-18).

### Review

Validators run first, and their report goes into every lens's brief as settled fact. The lenses then run in parallel, each read-only, each seeing only the group's diff, task list and card, never the builder's transcript.

| Lens | Covers | Validators alongside |
|---|---|---|
| Correctness | Bugs, logic flaws, business rules against the task list's `accept` lines and the PRD | Tests, reproducers |
| Security and readiness | Security, secrets, deployment readiness | Secret scan, dependency audit, language security linters |
| Quality | Code quality, conventions, blast radius | Linters, counts of callers of changed symbols |

Each lens checks against rule IDs in its checklist skill. The starting set:

| Lens | Rules |
|---|---|
| Correctness | COR-1 each task's `accept` line holds; COR-2 error paths are handled; COR-3 empty, zero and boundary inputs behave; COR-4 shared state is safe under concurrency; COR-5 the PRD's business rules hold |
| Security and readiness | SEC-1 input is validated where it crosses a trust boundary; SEC-2 authentication and authorisation are checked; SEC-3 no secret is logged or committed; SEC-4 no injection into queries, commands or paths; RDY-1 new configuration is documented and defaulted; RDY-2 migrations can roll back; RDY-3 new paths are observable |
| Quality | QUA-1 the language standard is followed; QUA-2 no dead code or duplicated logic; QUA-3 names say what the code does; QUA-4 tests cover changed behaviour; QUA-5 callers of changed exported symbols are updated |

Economy mode runs one session with the combined economy prompt. The conductor keeps a finding as blocking only when it verifies the evidence (REQ-20):

| Finding kind | Verified when |
|---|---|
| Bug, security | Its reproducer test fails on the current tree, in a scratch copy |
| Convention, quality | It cites a checklist rule ID and sits on a changed line |
| Performance, blast radius | A validator measured it |
| Anything else | Never blocks; it becomes a PR note |

A lens's Stop hook runs `komodo check findings` and refuses to let it finish until every blocking finding carries evidence, at most twice. After that, findings without evidence become notes.

### Repair

The conductor turns failed checks or verified findings into a fix list, one checkbox each, and resumes the builder's session with it (REQ-22). Check runs again. After a review-driven repair, the conductor resumes each lens session with the repair's diff. A re-review may only close its own earlier findings or flag lines the repair changed (REQ-21).

### Escalations

The conductor escalates when a builder is blocked, a group reaches its time limit, the progress rule stops a loop, or a stage fails in a way it can't retry. It sends the escalation to the orchestrator first (REQ-45). With a person present, the primary session handles it. On an unattended run, the conductor starts one headless orchestrator session per escalation. Either way, the orchestrator returns exactly one action:

| Action | Limits |
|---|---|
| Answer the builder's question | Only from the task list, the specs and the code; anything that changes scope is a stop instead |
| Split or clarify a task | The rewrite must pass `komodo lint` |
| Retry once on the heavy tier | Once per group |
| Stop the group | The conductor writes the blocker note, publishes a blocked draft PR, and waits for a person |

A headless run exits non-zero when it ends with any group blocked.

### Prepare and ship

Prepare runs locally with no model (REQ-24):

1. Commit the group's work with its ticked task list. When it is the last open group of its epic, also delete the epic's group files. The message is conventional, with no trailers.
2. Run the pre-commit and pre-push checks.
3. Rebase on the base. On a conflict, the conductor leaves the conflict markers in the worktree and runs one repair round with the conflicts as the fix list; the builder edits files and never runs git. If the conflict remains, the group stops with a blocker note.
4. Run the integration build and tests. The conductor also test-merges every group that is ready in the same run, to catch breakage between groups; a failure is a repair round for the group that caused it.
5. Plan the stack: a group targets its epic's branch, or the branch of a group it depends on, whichever it hasn't merged into yet (REQ-13, decision 0006).

Ship is the only stage that reads the forge credential (REQ-26):

1. If the group's epic has no branch yet, cut `feat/<epic's version>` from `main` and open it as a draft PR to `main`.
2. Push `refs/komodo/<branch>` to `refs/heads/<branch>` on origin, never a protected one; the push drops the builder's lease.
3. Open the group's PR against its base from step 5 above. If the forge refuses a draft, as GitHub Free does for private repos, open a normal PR labelled `status/wip` (REQ-25).
4. Add the labels. Once every check and review has passed, mark the PR ready and remove `status/wip`, then the conductor merges it into its epic branch. Only a person merges an epic branch's own PR into `main` (decision 0006).
5. When a stacked parent merges, rebase the child and point its PR at the new base.
6. Remove the group's worktree, its `refs/komodo` tip and sessions.

Ship also publishes a blocked group: it pushes the group's branch with its blocker note and opens a draft PR labelled `status/blocked`.

## User interface

Not applicable. There are no screens: people use the orchestrator session, `komodo status` and the backlog.

## Integrations

| System | Used for | Credential |
|---|---|---|
| Claude Code | The primary session, and every model session | The host's own login, including long-lived tokens for autonomous jobs |
| GitHub | Pushes, pull requests and labels at Ship; merge status for cleanup; the forge audit in `komodo doctor --remote` | The developer's local git PAT, read only by the conductor |
| OS sandbox | Confining line sessions: Seatbelt on macOS, bubblewrap on Linux and WSL2 | None |
| Plugins | Notifiers, tool packs and stage hooks, installed disabled | Per plugin, set when it is enabled |

GitHub Free offers draft PRs and rulesets only on public repositories; `komodo doctor --remote` says which a repo has. Package dependencies: none, since `go.mod` holds no `require` (decision 0002).

## Cross-cutting concerns

### Sessions: pinned and hermetic

| Variable | Pinned by |
|---|---|
| Host CLI version | Not pinned (decision 0001). `DISABLE_AUTOUPDATER` is set in each session's environment, so it never changes mid-run. |
| Model | Full IDs in the profile, such as `claude-sonnet-5` and `claude-opus-5-5` |
| Personal layer | `--setting-sources local` and `--strict-mcp-config`; no personal settings, instructions, plugins or MCP, and no shared project skills (REQ-3, decision 0001) |
| Rules and skills | The role's own plugin, plus the repo's `AGENTS.md` |
| `komodo` binary | A published release in product repos; rebuilt on pull in this repo (REQ-5) |
| Shell | POSIX `sh`: native on macOS, Linux and WSL2; Git Bash's on native Windows |
| Line endings | `* text=auto eol=lf` in the toolkit and in `komodo init`'s template |
| Toolchains | Declared by the repo (`go.mod` toolchain and the like); doctor checks them |

### Profiles and economy mode

A profile maps each role to a model and an effort. The conductor picks full or economy mode from the plan (decision 0009). The models are starting values; only economy mode changes them.

| Role | Full mode: Max plans and API billing | Economy mode: Pro plan |
|---|---|---|
| Builder, and every repair | Opus, medium effort (decision 0009) | Sonnet, medium effort |
| Correctness, and security and readiness lenses | Opus, high effort | One combined lens: Sonnet, high effort |
| Quality lens | Sonnet, medium effort | Part of the combined lens |
| Planner | Opus | Sonnet |
| Scout | Haiku | Haiku |
| Orchestrator on an escalation | Sonnet | Sonnet |

Economy mode also runs one group at a time, and uses its own combined review prompt rather than three. No builder runs on the light tier in either mode; doctor rejects a profile that puts it there.

### Skills and scoping

Each role runs with its own plugin directory and nothing else. The global layer in the primary session holds only the orchestrator's skills, so builder, review and standards skills never load there. Doctor measures each role's always-on context against its budget.

| Skill | Purpose | Roles | When it is used |
|---|---|---|---|
| `komodo` | The command reference, generated from `komodo help` | Orchestrator | Any time the line is driven |
| `run` | Start, watch, stop and resume runs; answer status questions | Orchestrator | `/run`, `/status`, `/stop`, `/resume` |
| `plan` | Turn the PRD and specs into task groups that pass lint | Orchestrator, planner | `/plan` |
| `escalate` | Settle an escalation with one allowed action | Orchestrator | On an escalation |
| `build` | Work a task list: order, checks, scope, finishing as blocked | Builder | Build, Repair |
| `review-correctness`, `review-security`, `review-quality` | One lens's checklist and evidence rules | Matching lens | Review |
| `review-economy` | All three checklists in one prompt | Reviewer, economy mode | Review on a Pro plan |
| `respond` | Answer a human's PR review threads | Responder | After a human review |
| `release` | Build, test and publish the binaries | Orchestrator, this repo only | `/release` |
| `standards-specs` | The four spec files | Planner, orchestrator | Planning |
| `standards-<language>` | One language's conventions | Builder and lenses, only for languages the group touches | Build, Review |

| Role | Profile tier | Tools |
|---|---|---|
| Orchestrator | The session's own model | Everything its allow list permits |
| Builder | heavy in full mode, standard in economy mode; never light | Read, Edit, Write, Bash, Grep, Glob |
| Review lens | reviewer, or standard for Quality | Read, Grep, Glob |
| Planner | heavy | Read, Grep, Glob |
| Scout | light | Read, Grep, Glob; answers the orchestrator's lookups |
| Responder | standard | Read, Edit, Write, Bash, Grep, Glob |

The architect, researcher, summarizer and tester roles stay available to the orchestrator for ad hoc work. The line never starts them.

### Hooks

Most loops in the first line came from hooks and guards: 187 builder refusals, and review rounds that kept finding new guard bypasses. Every hook now follows one contract (decision 0008):

| Hook | Session | Checks one thing | On a violation | Limit | If the hook itself fails |
|---|---|---|---|---|---|
| Guard, PreToolUse, global tier | Every session | Critical refs, force push, `--no-verify`, host and toolkit config paths, attaching a branch in a linked worktree, a push to a branch a live builder's lease holds | Refuses, naming the allowed alternative | — | Allows and logs |
| Gate, commit-msg | Every committer, model or not | The message carries no trailer the loaded policy names | Refuses, naming the trailer to remove | — | Fails, naming the missing binary |
| Gate, pre-commit | Every committer, model or not | The branch, or a detached worktree's tracked branch, is not critical, and is `<type>/<kebab-name>`, an epic branch, a line-cut slug, or detached | Refuses, naming the branch to rename | — | Fails, naming the missing binary |
| Gate, pre-push | Every pusher, model or not | The pushed ref is not critical, and no builder's lease holds it | Refuses, naming the lease's group, pid and lapse time | — | Fails, naming the missing binary |
| Guard, PreToolUse, line tier | A session `KOMODO_ROLE` names (decision 0008) | The global tier, plus writes outside the worktree, isolated spawns, and the epic branch's push and merge | Refuses, naming the allowed alternative | 3 refusals of one rule per session, then the session ends as blocked | Allows and logs |
| Format, PostToolUse on edits | Builder | Formats the edited file and lints only that file | Never refuses; returns lint output as context | — | Skips |
| Task checks, Stop | Builder | The group's checks pass | Refuses to stop, with the failing output | 3, the host's stop-hook cap | Allows; Check still reruns everything |
| Evidence, Stop | Review lens | Every blocking finding carries evidence | Refuses to stop, listing the findings without evidence | 2, then those findings become notes | Allows |
| Time warning, PostToolUse | Builder, lens | Time and turns used | Never refuses; warns at 80% | — | Skips |
| Status, SessionStart | Orchestrator | — | Adds the run's status and any blocked groups | — | Skips |
| Prune, SessionStart | Orchestrator | — | Launches the sweep detached, one at a time, capped at 2 minutes, logged to `.komodo/prune.log`: it publishes the newest binary, re-renders this repo's layer and an installed global layer, then prunes finished worktrees; names the last sweep's failure | — | Skips |

**Every hook registers once, and every hook runs `~/.komodo/bin/komodo`.** An interactive session takes the guard from the user's global layer; the repo's `.claude/settings.json` registers no hook. A line session loads only `.claude/line-settings.json` (`--setting-sources local`), which carries the guard. So no tool call is judged twice.

The boundaries that stop hook loops:

- **Hooks judge only a model's tool calls.** Commands the conductor runs are never hooked.
- **No hook parses a command for hidden intent.** Containment is the sandbox's job, and the output checks catch the rest.
- **Every refusal names what to do instead.** A refusal with no way forward is a bug.
- **A repeated refusal ends the session as blocked,** so the model never tries variant after variant.
- **A guard-bypass finding never blocks a review.**

### Permissions

Each role's settings carry an allow list that covers everything its stage needs (REQ-38, decision 0008). Git housekeeping belongs to the conductor, so no agent ever needs it.

| Role | Allowed without asking | Refused | Done by the conductor instead |
|---|---|---|---|
| Builder | Reading, searching, creating, editing, moving and deleting files in the worktree; read-only git (`status`, `diff`, `log`, `show`, `blame`); the repo's build, test, lint and format commands; `komodo check` | Git writes (commit, switch, reset, rebase, merge, stash, push); network tools beyond the sandbox allowlist; anything outside the worktree | Commits, branches, syncing with the base, conflict setup, cleanup |
| Review lens | Reading, searching, read-only git | Every edit and every git write | Running reproducers and validators |
| Orchestrator | Everything in the current repo, including this repo's rules, skills and guard source; switching to `main` and fast-forwarding it; creating and deleting feature branches; cutting detached worktrees with `komodo worktree add`; spawning its own isolated agents for ad hoc work; `komodo`; read-only `gh` | Commits, pushes, merges, deletes or force on `main`; commit trailers; hand edits to `.git/config` and `.git/hooks`; attaching a branch in a linked worktree; a push to a branch a builder's lease holds | Shipping, through `komodo ship` |

### Cross-platform: macOS, Linux, Windows

Native Windows comes first, and WSL2 is used when present (decision 0002). WSL2 is the Linux environment built into Windows 10 and 11.

| Concern | macOS | Linux | Windows, native | Windows, WSL2 |
|---|---|---|---|---|
| Binary | `darwin/arm64`, `darwin/amd64` | `linux/amd64`, `linux/arm64` | `windows/amd64` | The Linux binary |
| Sandbox | Seatbelt | bubblewrap | None | bubblewrap |
| Shell | `sh` | `sh` | Git Bash's `sh` | `sh` |
| Killing a process tree | Process group | Process group | A job object | Process group |
| Installer | `install.sh` | `install.sh` | `install.ps1` | `install.sh` |
| Repo location | Anywhere | Anywhere | Anywhere | The Linux filesystem, never `/mnt/c` |

### Pacing, limits and loop detection

- **Bound or unbound.** The plan probe (`internal/mount/claude/limits.go`) and the host's `rate_limit_event` stream messages give the plan and its usage windows. On a subscription the conductor paces to the windows and pauses until the reset (REQ-32). On API billing, a spend budget applies instead.
- **Concurrency.** Groups at once, as starting values: Pro 1, Max 5x 4, Max 20x 6, API 4, from the mount's own `Concurrency(plan)`.
- **Time.** A group has 60 minutes (REQ-29). Session starting values: build 25 minutes, each lens 8, each repair 10, each re-review 5.
- **Loop detection.** The conductor stops a group and escalates when:
  - a round leaves the open findings unchanged
  - a repair changes no file
  - a check fails identically after a repair
  - a guard rule reaches its refusal limit

### Parallelism

| What | Runs in parallel when |
|---|---|
| Task groups | They share no file; up to the plan's concurrency (REQ-12) |
| Review lenses | Always, in full mode |
| Checks and validators | Per package, inside Check and Review |
| Context packing | Per group, during Coordinate |
| Stages across groups | One group can build while another is in review or preparing |

### Token efficiency

- **Five of the eight stages spend no tokens.** Only Build, Review and Repair run models.
- **Context packs replace exploration.** The builder starts with the files, signatures and callers it needs.
- **Scoped roles.** Each session loads only its own plugin, and the primary session loads none of the line's skills.
- **Cache-friendly briefs.** Stable slots come first, so sessions share cached prompt prefixes.
- **Resume, don't restart.** Repair and re-review resume their sessions. The headline metric is tokens per accepted group, reported by every eval.

### Security

The guard keeps four rules:
1. no commit, push, merge, delete or force on a critical ref
2. no push that rewrites history
3. no skipping git hooks
4. no edit or write outside the worktree

A commit's trailer and its branch name are refused by the gate's commit-msg and pre-commit git hooks instead, so every committer answers to them, model or not, not only a tool call the guard can see.

Its matcher covers every host tool that runs a command: Bash, PowerShell and Monitor. It does not stop links, command launchers, HTTP forge writes, or expansion it cannot see at runtime. The sandbox, where the platform has one, and the absent forge credential hold against those.

The forge credential stays with the conductor. Ship reads it from the developer's git credential store, or from `gh`, and gives it only to its own push. Every model session starts from a scrubbed environment, and the sandbox's network allowlist excludes the forge.

In this repo, the orchestrator may edit the guard, the policy and the skills on a branch (decision 0008). A change takes effect only after a human merges it and the binary rebuilds, so a session never loosens its own guard. A builder may edit those files only when its task list names them.

## Operations

### Install

`komodo install` is the one setup step, the same on every platform, and safe to run again. From a checkout with Go, run it as `go run komodo/cmd/komodo install`; elsewhere `install.sh` (macOS, Linux, WSL2) or `install.ps1` (native Windows) downloads the pinned release, verifies its SHA-256 and runs it. Those two scripts hold no other logic, and `komodo lint` keeps every other committed script to one exec into komodo. The command:

1. Refuses a repo on the Windows filesystem under WSL2, naming where to clone it.
2. In the toolkit's own checkout, builds `bin/komodo` when it is missing.
3. Publishes the binary to `~/.komodo/bin/komodo`, the one path every hook runs, and links `komodo` onto PATH: a symlink in `~/.local/bin` on macOS and Linux; on Windows `install.ps1` puts the binary's own directory on the user PATH.
4. In the toolkit's own checkout, writes the git hooks: each is one exec into `komodo git-hook <name>`, which holds the hook's logic.
5. Renders this repo's layer, then the user's global layer. Outside a repo it installs only the global layer; `komodo init` adds the line to a new repo.

### Binaries and releases

- **This repo rebuilds itself.** The gate installs post-merge, post-checkout and post-rewrite hooks alongside pre-commit, commit-msg and pre-push. When Go sources changed, they rebuild `bin/`, so nobody runs a command to get the latest binary (REQ-5, decision 0006).
- **A build is reproducible.** `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and `-ldflags "-s -w"` plus the changelog version and commit make a rebuild of one commit byte-identical (decision 0002); `GOTOOLCHAIN` is pinned to `go.mod`'s `toolchain` line, and `komodo version` prints what a binary was built from.
- **`komodo release` publishes.** On the owner's machine it cross-compiles every platform into `dist/`, runs the tests, writes checksums and publishes a GitHub Release. No forge CI runs.
- **The `release` skill** drives it from the orchestrator, including the version bump and changelog.

### Health checks

`komodo doctor` runs before every run as part of preflight (REQ-6), and on demand:

| Check | Fails when |
|---|---|
| Pins | A model ID, the `komodo` release or a toolchain differs from the profile |
| Hermetic config | The line's config directory is missing, or holds personal settings, plugins or MCP |
| Sandbox | The platform has one and it can't start |
| Budgets | A role's always-on context is over its budget |
| Leftovers | An ended epic's files, or a merged group's worktree or branch, remain |
| Plugins | A plugin's manifest is malformed; each plugin's enabled or disabled state is listed |
| Forge, with `--remote` | No ruleset where the forge offers one; head branches aren't deleted on merge; drafts are unavailable (a warning) |

### Plugins

A plugin is a folder with a manifest naming its type, the roles and stages it attaches to, and its settings. V1 ships the three types disabled (REQ-42), and runs none of them: a machine may enable one, but its point is wired in only by the release that ships the first real plugin (decision 0013).

- **Notifiers:** copy blocker notes and run summaries somewhere else, such as Slack or Google Chat later. They never decide anything.
- **Tool packs:** mechanical commands, such as cloud CLIs for AWS, GCP or Azure, added to a role's allow list behind the guard.
- **Stage hooks:** commands run before or after a stage, which can stop the group with a reason.

### Rollout

Each phase is a patch on the current code: `Next`, the stations and the ledger stay. Estimates are rough. Requirement IDs are defined in `docs/prd.md#requirements`. Phases 0 to 3 ship as `1.0.0-alpha` releases, phase 4 as betas, and `1.0.0` is the LTS release (decision 0010).

| Phase | Requirements | Work | Exit |
|---|---|---|---|
| 0. Stabilize, days | REQ-4, REQ-5, REQ-13 | Fix the 2 regressions #201 merged: the guard judging line-run commands, and the gate's silent skip. Freeze the guard. Add `.gitattributes` and the rebuild-on-pull hooks. Trim finished tasks from `BACKLOG.md`, since `CHANGELOG.md` holds their history. Move the README's design sections into these specs. | The gate is green, `main` is every group's base, and a pull rebuilds the binary |
| 1. Conductor and sessions, week 1 | REQ-2, REQ-3, REQ-6, REQ-11, REQ-14–REQ-16, REQ-28, REQ-29, REQ-31 | The conductor drives the stages. Per-role plugins, hermetic config, preflight, per-run metrics, time limits. The run skill becomes a launcher. | One group runs through the conductor within 60 minutes, with zero conductor tokens |
| 2. Guardrails, week 2 | REQ-17, REQ-26, REQ-33–REQ-38, REQ-40, REQ-41 | The hook contract, allow lists, output checks, credential isolation, the sandbox where available, the guard cut to five rules | Every safety proof passes |
| 3. Groups, review and repair, weeks 2–3 | REQ-7–REQ-10, REQ-12, REQ-18–REQ-25, REQ-27, REQ-30, REQ-32, REQ-45, REQ-46 | The backlog files, cleanup and blocker notes; group cards and checkboxes; parallel lenses and evidence checks; resumed repair and re-review; the progress rule; escalations; draft-first shipping; pacing. Open groups move from `BACKLOG.md` into `docs/backlog/`. | A 3-group plan runs unattended to draft PRs |
| 4. Install, platforms and eval, weeks 3–4 | REQ-1, REQ-39, REQ-42–REQ-44 | Install scripts, native Windows and WSL2, the release command, plugin points, the golden suite and `komodo eval` | The success criteria hold on all three platforms, and the owner cuts 1.0.0 |

A phase starts once the spikes its decisions name have passed. Work the PRD scopes out of 1.0.0 lands nothing new.

## Recovery

Every loop ends on its own, and every stop leaves work that a person or the next run can pick up.

### Convergence rules

- **Blocking needs evidence.** A finding the conductor can't verify never blocks, whatever its severity.
- **Critical without evidence goes to a person.** The group ships as a draft PR with the finding at the top.
- **A re-review can't widen.** It may only close its own findings or flag lines the repair changed.
- **Progress or stop.** A round that closes no finding ends the loop, and the group ships as a draft with its open findings listed (REQ-23).
- **The clock.** At 60 minutes the conductor stops the group and escalates (REQ-29).

### Stopped and blocked work

Whatever stops a group, the conductor first saves it. It commits the worktree's changes as a local WIP commit on the group branch, never pushed, and records the state in `state.json`. Only the tasks that passed Check stay ticked.

| Stop | Worktree and branch | Backlog | Dependents | What resumes it |
|---|---|---|---|---|
| Blocked, and the orchestrator settled it | Kept | Unchanged | Wait briefly | The orchestrator's action |
| Blocked, and the orchestrator couldn't settle it | Kept, with a WIP commit, pushed as a draft PR labelled `status/blocked` | BLOCKED, with a blocker note on the group's branch | Wait | A person edits the group and sets it READY |
| Time limit reached | Kept, with a WIP commit | A blocker note, unless the orchestrator settles it | Wait | As above |
| Host error or crash | Kept, with a WIP commit | Unchanged | Wait | `komodo resume`: the session is resumed, or a fresh one starts from the WIP commit and the task list |
| Run killed | Kept, with a WIP commit where possible | Unchanged | Wait | `komodo resume` (REQ-14) |
| Stopped before Ship | Kept, fully committed | Every task ticked | Wait | `komodo ship` |

Other groups keep running. A worktree is removed only after its group ships or someone runs `komodo abandon`. A group that stops twice without progress gets a blocker note, whatever the orchestrator says.

### Run failures

| Failure | What the line does |
|---|---|
| No forge credential at preflight | `komodo run` refuses and names the fix; `--no-ship` runs anyway and stops each group before Ship |
| The credential fails at Ship | The group stops before Ship with a blocker note on its branch; with no credential it can't be published, so `komodo status` and the orchestrator show it. Other groups continue (REQ-27). |
| The host login fails | The run stops before any session starts and names the fix |
| A usage limit is reached | The run pauses until the window resets, then continues |
| The sandbox is missing where the platform has one | `komodo run` refuses (REQ-35) |
| The gate detects no build check | It fails loudly and names the `.komodo/commands.json` line that fixes it |

## Testing

| Tier | Proves | Command |
|---|---|---|
| Unit and table tests | Each package, the guard table, the conductor's decisions | `go test ./...` |
| The gate | Vet, race tests, doctor, the guard table, the comment lint | `komodo gate` |
| Fuzz | The guard, the backlog parser and the ledger reader never panic or disagree | `komodo gate --fuzz 10s`, on push |
| Eval | The whole line on golden groups, on each platform | `komodo eval --runs 3` |

The eval:

- **Golden repos and groups:** real Komodo repos at pinned commits, sized by REQ-44, picked by the owner at the time of the eval. Each group is rebuilt from real merged commits: reset to the parent, write the task list from the change's intent, and hide the change's own tests from the builder.
- **A run:** each group in a fresh clone, through the whole line, with Ship replaced by a local no-push. Then the hidden tests run.
- **The report:** pass rate, consistency, sessions, turns, tokens, minutes, review rounds, and tokens per accepted group, per platform. The thresholds are `prd.md#success-criteria`.
- **The lock:** the suite, its thresholds and `docs/prd.md` are denied to line sessions (REQ-41).
