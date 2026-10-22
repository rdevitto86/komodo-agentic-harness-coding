# komodo-agentic-factory-coding

Komodo's code assembly line. Work enters as tasks and leaves as reviewed pull requests. The line is one static binary and a set of markdown files; the machines on it are whatever models you mount today. Swap a model and the line does not change. Swap the host and one mount changes.

This README is the day-to-day guide; each Design bullet points into the specs that define V1: `docs/prd.md`, the requirements, with `docs/hld.md`, `docs/lld.md` and `docs/decisions/`. V1 restarts at `1.0.0-alpha.5` and moves through its betas to the `1.0.0` LTS release the human cuts (decision 0010); the Versions section below defines each stage. Everything before it was a prototype: the 0.x experiments and the Python orchestrator, now tagged `1.0.0-alpha.1` through `1.0.0-alpha.4`, preserved whole at the tag `prototype-final`. The repo was cleared to the markdown source on 2026-09-21, and the prototype's run state did not survive the clear.

## Design

The full design lives in `docs/hld.md` and `docs/lld.md`; `docs/decisions/` holds why. This file keeps only what a developer needs to run the line day to day.

- **The line.** One binary conveys work through eight stages, with two machines and one mount per host; see `docs/hld.md#components` and the flow in `docs/diagrams/assembly-line.svg`.
- **Stations.** Ingest, Coordinate, Build, Check, Review, Repair, Prepare and Ship; see `docs/hld.md#components`.
- **Devices.** The brief in, the schema-checked result out, each slot capped; see `docs/lld.md#briefs`.
- **Metrics.** Every stage writes to the run's local, gitignored ledger; see `docs/lld.md#run-state-and-metrics`.
- **Machines and mounts.** A profile maps each role's tier to a model and effort, per plan and per host; see `docs/lld.md#profiles-and-economy-mode`.
- **Ad hoc work.** The line is captive to `/run`; ad hoc work is the orchestrator spawning its own default agents outside it, with no skill of its own; see `docs/lld.md#orchestrator-commands`.
- **The guard.** One hook holds five rules on every tool call, on every host; see `docs/lld.md#security`.
- **The binary.** `komodo` is one static Go binary, rebuilt on pull in this repo; see `docs/lld.md#binaries-and-releases`.
- **The gate.** Local build, lint and doctor checks run before every commit and push, no model, nothing on GitHub; see `docs/lld.md#binaries-and-releases`.
- **The repo layer.** A repo may commit context, standards, skills and command overrides under `.komodo/`; see `docs/lld.md#the-repo-layer`.
- **Detection and facets.** Cloud facet work beyond the shipped set is out of scope until after 1.0.0; see `docs/prd.md#product-scope`.
- **Local machines.** A local model is out of scope until after 1.0.0; see `docs/prd.md#product-scope`.
- **Hot swap.** A role's model swaps through its profile without touching the line; MCP is out of scope until after 1.0.0; see `docs/lld.md#profiles-and-economy-mode` and `docs/prd.md#product-scope`.
- **The non-proprietary day.** 1.0.0 proves one host; a second host is out of scope until after 1.0.0; see `docs/prd.md#product-scope`.

## Pull requests

1.0 was built through PR #103 and six stacked group PRs, now merged. Each group ships as one PR from its own `<type>/<slug>` branch, cut from the group's base. `close --group` opens it with the report as the body. Merging is the human's button; nothing runs on GitHub.

The repository ruleset must cover `main` only, which `komodo doctor --remote` audits; it also fails when no ruleset or branch protection covers `main` at all.

## Versions

Every version here is SemVer with a prerelease stage, and a group's `version:` matches its changelog heading exactly. `komodo/rules/backlog.md#choosing-a-version` is the rule a planner follows to pick a group's segment and phase; this section names only this repo's own history through each one.

- **Alpha, `x.y.z-alpha.n`.** The shape still moves. V1 restarts the rebuild at `1.0.0-alpha.5` while phases 0 to 3 land; the prototype's four releases are renumbered `1.0.0-alpha.1`–`.4` (decision 0010).
- **Beta, `x.y.z-beta.n`.** Feature-complete for `x.y.z`; only fixes land while `komodo eval` runs on every platform. V1's beta starts at `1.0.0-beta.2`, since the untagged `1.0.0-beta.1` heading is retitled as history and never reused (decision 0010).
- **Rc, `x.y.z-rc.n`, optional.** Nothing requires, gates, or checks it; a release may go straight from beta to stable. V1 takes that path (decision 0010).
- **Stable, `x.y.z`.** The LTS release, cut by the owner once `docs/prd.md#success-criteria` holds. `komodo tag` never promotes a beta on its own.

## Names

One vocabulary, used the same way in this file, the backlog, the code, the skills, and every command and flag. The prototype's words for these parts are retired, and TSK-03.6.2 greps them out of everything a model or a developer reads.

| Name | Means |
|---|---|
| Station | One fixed step of the line, a `komodo` subcommand or a spawn |
| Device | The brief going in, the result JSON coming out |
| Machine | A model doing one station's work |
| Mount | The code that carries a brief to a machine on one host, or to Ollama |
| Profile | The table that maps tiers to machines for one host |
| Tier | light, standard, or heavy; what a role asks for, never a model |
| Role | One markdown file: what a machine is at a station or in a session |
| Skill | A markdown procedure a session or a brief can load |
| Facet | What detection selects for a platform: a skill, appendices, commands |
| Guard | The one agent hook on shell, edit, write, and spawn |
| Gate | The local precheck before a commit and a push |
| Ledger | The two local metrics files |

## Setup

Requirements: git, `gh` authenticated, and the host CLI on PATH. Ollama is optional; set `"local": true` in `~/.komodo/config.json` and, once it answers, the light tier moves to it and the reviewer stays remote unless the overlay also says `"local_reviewer": true`.

```bash
git clone <this repo> ~/komodo/ai/komodo-agentic-factory-coding
cd ~/komodo/ai/komodo-agentic-factory-coding
go run komodo/cmd/komodo install                  # the one step: binary, git hooks, this repo, and your machine
```

That one command builds `bin/komodo`, publishes it to `~/.komodo/bin/komodo`, which every hook runs, links `komodo` onto PATH, writes the git hooks, and installs this repo's and your user's Claude layers. It is the same on macOS, Linux and Windows. After that nothing needs rerunning: each rebuild republishes the binary, and each session start re-renders a stale layer in the background. Without Go, `install.sh` or `install.ps1` downloads and verifies a release and runs the same `komodo install`.

### Start a new repo

From the root of the new repo, a git repository:

```bash
komodo init --name "Auth API"      # AGENTS.md, docs/backlog, CHANGELOG.md, docs/ specs, the PR template; keeps any file that exists
komodo install --host claude       # mount the repo on a host
$EDITOR docs/backlog/TG-01.1-example-group.md  # replace the example group with the first real one; komodo lint checks it
komodo run                         # drive the line on the next ready group
```

## Usage

```bash
komodo run                  # headless, credentials stripped: drains every ready group
komodo run TG-03.5          # headless, one group
/run                        # in a session: the next ready group down the line
/run TG-03.5                # one group
/run TSK-03.5.2             # one task
/review                     # QC and the reviewer on the current diff
komodo next --json          # what would run, and why
komodo lint                 # after every backlog edit
komodo doctor               # references, portability, drift, prune; --remote audits the forge's rulesets
komodo gate                 # build checks, lint, doctor, guard table, comments; pre-commit skips tests, pre-push runs all
```

With no target, `komodo run` drains every ready group in order: for each it builds
the tasks, repairs review findings for up to `review_repairs` rounds, re-renders
the host config when doctor reports drift, and opens one pull request per group.
Merging is the only step a person does across a clean drain; `komodo sync`
follows it automatically, fast-forwarding the root and rebuilding what drifted.

A drain never stops at one group. A group that ends unshipped, such as a merge
conflict QC cannot resolve or a plan the profile has paused, is parked with its
reason, and the drain runs the next. It ends when nothing is ready or the budget
is spent, listing what it shipped and what it parked.

## Layout

| Path | What |
|---|---|
| `komodo/AGENTS.md`, `komodo/rules/` | Universal rules, the accessibility contract, the backlog grammar |
| `komodo/roles/` | One file per role: tier, tools, session flag, schema, brief template |
| `komodo/skills/` | `run`, `review`, `backlog`, `respond`, and one `standards-<x>` per language or domain |
| `komodo/policy.json` | The critical refs, config paths, and trailer patterns the guard reads |
| `komodo/facets/` | One directory per platform: Komodo's setup skill, appendices, commands, markers |
| `cmd/komodo/`, `internal/` | The binary: line, guard, mounts including Ollama, gate, launcher |
| `bin/` | Gitignored local build output, built by `komodo gate --install` |
| `templates/project/` | Starters `komodo init` writes into a new repo |
